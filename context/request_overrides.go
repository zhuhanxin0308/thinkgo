package context

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// ErrRequestOverrideConflict 表示同一批请求覆盖包含多个映射到相同名称的原始键。
var ErrRequestOverrideConflict = errors.New("请求覆盖包含重复名称")

// WithHeader 替换请求头。重复头名优先取 HTTP 规范拼写，其次取字典序最小的原始键。
// 重复同值也会产生可从 OverrideError 读取的诊断。
func (r *Request) WithHeader(values map[string]string) *Request {
	_ = r.replaceHeader(values, false)
	return r
}

// WithHeaderStrict 替换请求头；重复规范名称（即使同值）时返回 ErrRequestOverrideConflict，
// 并保留原有请求头和诊断。空集合会显式清空请求头。
func (r *Request) WithHeaderStrict(values map[string]string) error {
	return r.replaceHeader(values, true)
}

func (r *Request) replaceHeader(values map[string]string, strict bool) error {
	if r == nil {
		return errors.New("请求不能为空")
	}
	keys, err := requestOverrideKeys(values, "Header", http.CanonicalHeaderKey)
	if strict && err != nil {
		return err
	}
	headers := make(http.Header, len(keys))
	for canonical, original := range keys {
		headers[canonical] = []string{values[original]}
	}
	state := r.thinkPHPState()
	state.mu.Lock()
	state.headerValues, state.headerSet, state.headerErr = headers, true, err
	state.mu.Unlock()
	return err
}

// WithServer 替换 SERVER 数据。重复键优先取去掉首尾空白的大写规范拼写，
// 其次取字典序最小的原始键。重复同值也会产生可从 OverrideError 读取的诊断。
func (r *Request) WithServer(values map[string]interface{}) *Request {
	_ = r.replaceServer(values, false)
	return r
}

// WithServerStrict 替换 SERVER 数据；重复规范名称（即使同值）时返回 ErrRequestOverrideConflict，
// 并保留原有 SERVER 数据、派生的入口脚本缓存和诊断。空集合会显式清空 SERVER。
func (r *Request) WithServerStrict(values map[string]interface{}) error {
	return r.replaceServer(values, true)
}

func (r *Request) replaceServer(values map[string]interface{}, strict bool) error {
	if r == nil {
		return errors.New("请求不能为空")
	}
	keys, err := requestOverrideKeys(values, "SERVER", canonicalServerKey)
	if strict && err != nil {
		return err
	}
	server := make(map[string]interface{}, len(keys))
	for canonical, original := range keys {
		server[canonical] = deepCloneRequestValue(values[original])
	}
	state := r.thinkPHPState()
	state.mu.Lock()
	state.serverValues, state.serverSet, state.serverErr = server, true, err
	state.baseFile, state.baseFileSet = "", false
	state.mu.Unlock()
	return err
}

// OverrideError 返回当前已发布 Header 和 SERVER 覆盖的冲突，可用 errors.Is 检查
// ErrRequestOverrideConflict。成功替换一种来源只清除该来源的诊断。
func (r *Request) OverrideError() error {
	state := r.peekThinkPHPState()
	if state == nil {
		return nil
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return errors.Join(state.headerErr, state.serverErr)
}

func canonicalServerKey(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
}

// requestOverrideKeys 只检查键，不比较任意类型的 SERVER 值，也不把值写入诊断。
func requestOverrideKeys[T any](values map[string]T, source string, normalize func(string) string) (map[string]string, error) {
	originals := requestSourceKeys(values)
	slices.Sort(originals)
	selected := make(map[string]string, len(values))
	var conflict error
	for _, original := range originals {
		canonical := normalize(original)
		if _, exists := selected[canonical]; exists {
			if conflict == nil {
				conflict = fmt.Errorf("%w: %s %q", ErrRequestOverrideConflict, source, canonical)
			}
			if original != canonical {
				continue
			}
		}
		selected[canonical] = original
	}
	return selected, conflict
}
