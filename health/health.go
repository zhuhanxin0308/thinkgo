// Package health 提供进程内存活与就绪检查能力，不自动注册任何 HTTP 路径。
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	defaultTimeout          = 2 * time.Second
	maxCheckName            = 128
	maximumConcurrentChecks = 4

	StatusOK   = "ok"
	StatusFail = "fail"
)

var (
	// ErrInvalidCheckName 表示检查名称不符合稳定标识约束。
	ErrInvalidCheckName = errors.New("无效健康检查名称")
	// ErrInvalidCheck 表示检查函数为空或重复注册。
	ErrInvalidCheck = errors.New("无效健康检查")
	// ErrInvalidTimeout 表示健康检查总超时不是正数。
	ErrInvalidTimeout = errors.New("无效健康检查超时")
	// ErrCheckExecutionLimit 表示已有不协作或耗时检查占满了受控执行槽。
	ErrCheckExecutionLimit = errors.New("健康检查执行槽已耗尽")
)

// Check 是一个应尊重 context 取消信号的外部依赖检查函数。
type Check func(context.Context) error

// CheckResult 是单项健康检查的安全摘要，Error 仅供进程内调用方查看，不会被 JSON 导出。
type CheckResult struct {
	Status     string  `json:"status"`
	DurationMS float64 `json:"duration_ms"`
	Error      error   `json:"-"`
}

// Report 是一次健康检查报告。Checks 的键顺序在 JSON 导出时稳定。
type Report struct {
	Status     string                 `json:"status"`
	DurationMS float64                `json:"duration_ms"`
	Checks     map[string]CheckResult `json:"checks,omitempty"`
}

// Healthy 表示报告中的所有检查均已通过。
func (r Report) Healthy() bool {
	return r.Status == StatusOK
}

// HTTPStatus 返回适合健康端点的 HTTP 状态码。
func (r Report) HTTPStatus() int {
	if r.Healthy() {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}

// Registry 管理一组有序、低基数的就绪检查。
type Registry struct {
	mu         sync.RWMutex
	checks     map[string]Check
	timeout    time.Duration
	checkSlots chan struct{}
}

// NewRegistry 创建默认总超时为两秒的健康检查注册表。
func NewRegistry() *Registry {
	return &Registry{
		checks:     make(map[string]Check),
		timeout:    defaultTimeout,
		checkSlots: make(chan struct{}, maximumConcurrentChecks),
	}
}

// SetTimeout 设置一次就绪检查的总预算；不协作的检查会被硬性隔离在受控后台槽中。
func (r *Registry) SetTimeout(timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("%w: %s", ErrInvalidTimeout, timeout)
	}
	if r == nil {
		return fmt.Errorf("%w: 注册表为空", ErrInvalidTimeout)
	}
	r.mu.Lock()
	r.timeout = timeout
	r.mu.Unlock()
	return nil
}

// Timeout 返回当前检查总预算。
func (r *Registry) Timeout() time.Duration {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	timeout := r.timeout
	r.mu.RUnlock()
	return timeout
}

// Register 注册或替换一个检查；重复名称被拒绝，避免部署顺序改变检查语义。
func (r *Registry) Register(name string, check Check) error {
	if r == nil {
		return fmt.Errorf("%w: 注册表为空", ErrInvalidCheck)
	}
	if err := validateCheckName(name); err != nil {
		return err
	}
	if check == nil {
		return fmt.Errorf("%w: %q 的函数为空", ErrInvalidCheck, name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.checks == nil {
		r.checks = make(map[string]Check)
	}
	if _, exists := r.checks[name]; exists {
		return fmt.Errorf("%w: %q 已存在", ErrInvalidCheck, name)
	}
	r.checks[name] = check
	return nil
}

// Unregister 移除一个检查并返回是否实际移除。
func (r *Registry) Unregister(name string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.checks[name]; !exists {
		return false
	}
	delete(r.checks, name)
	return true
}

// Liveness 返回进程本身存活的报告，不执行外部依赖检查。
func (r *Registry) Liveness() Report {
	if r == nil {
		return Report{Status: StatusFail}
	}
	return Report{Status: StatusOK, Checks: map[string]CheckResult{}}
}

// Readiness 执行全部检查；任何失败、panic 或超时都会使报告失败。
func (r *Registry) Readiness(parent context.Context) Report {
	started := time.Now()
	if r == nil {
		return Report{Status: StatusFail, DurationMS: durationMilliseconds(time.Since(started))}
	}
	if parent == nil {
		parent = context.Background()
	}
	checks, timeout := r.snapshotChecks()
	checkContext, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	report := Report{Status: StatusOK, Checks: make(map[string]CheckResult, len(checks))}
	for _, entry := range checks {
		if contextErr := checkContext.Err(); contextErr != nil {
			report.Status = StatusFail
			report.Checks[entry.name] = CheckResult{Status: StatusFail, Error: contextErr}
			continue
		}
		checkStarted := time.Now()
		err := r.runCheckWithinBudget(entry.check, checkContext)
		if err == nil && checkContext.Err() != nil {
			err = checkContext.Err()
		}
		result := CheckResult{Status: StatusOK, DurationMS: durationMilliseconds(time.Since(checkStarted))}
		if err != nil {
			result.Status = StatusFail
			result.Error = err
			report.Status = StatusFail
		}
		report.Checks[entry.name] = result
	}
	report.DurationMS = durationMilliseconds(time.Since(started))
	return report
}

// LivenessHandler 创建存活探针处理器。
func (r *Registry) LivenessHandler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeReport(writer, r.Liveness())
	})
}

// ReadinessHandler 创建就绪探针处理器，失败时返回 503 且不暴露底层错误文本。
func (r *Registry) ReadinessHandler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var parent context.Context
		if request == nil {
			parent = context.Background()
		} else {
			parent = request.Context()
		}
		writeReport(writer, r.Readiness(parent))
	})
}

type namedCheck struct {
	name  string
	check Check
}

func (r *Registry) snapshotChecks() ([]namedCheck, time.Duration) {
	r.mu.RLock()
	checks := make([]namedCheck, 0, len(r.checks))
	for name, check := range r.checks {
		checks = append(checks, namedCheck{name: name, check: check})
	}
	timeout := r.timeout
	r.mu.RUnlock()
	sort.Slice(checks, func(i, j int) bool { return checks[i].name < checks[j].name })
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return checks, timeout
}

func runCheck(check Check, ctx context.Context) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("健康检查 panic: %v", recovered)
		}
	}()
	return check(ctx)
}

// runCheckWithinBudget 通过有界执行槽隔离不遵守 Context 的检查，防止探针被永久阻塞或无界泄漏 goroutine。
func (r *Registry) runCheckWithinBudget(check Check, ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	slots := r.executionSlots()
	select {
	case slots <- struct{}{}:
		// 只有取得执行槽后才启动检查，避免并发探针无限创建后台任务。
	default:
		return ErrCheckExecutionLimit
	}

	result := make(chan error, 1)
	go func() {
		defer func() { <-slots }()
		result <- runCheck(check, ctx)
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Registry) executionSlots() chan struct{} {
	r.mu.Lock()
	if r.checkSlots == nil {
		r.checkSlots = make(chan struct{}, maximumConcurrentChecks)
	}
	slots := r.checkSlots
	r.mu.Unlock()
	return slots
}

func writeReport(writer http.ResponseWriter, report Report) {
	if writer == nil {
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(report.HTTPStatus())
	// Report 中的 Error 标记带有 json:"-"，不会把数据库地址、凭据或内部路径返回给探针调用方。
	_ = json.NewEncoder(writer).Encode(report)
}

func validateCheckName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || len(name) > maxCheckName {
		return fmt.Errorf("%w: %q", ErrInvalidCheckName, name)
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: 名称包含控制字符", ErrInvalidCheckName)
		}
	}
	return nil
}

func durationMilliseconds(duration time.Duration) float64 {
	return float64(duration.Nanoseconds()) / float64(time.Millisecond)
}
