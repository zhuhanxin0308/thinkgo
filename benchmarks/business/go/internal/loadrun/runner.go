package loadrun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type scheduledRequest struct {
	Sequence uint64
	When     time.Time
	End      time.Time
}

// Run 仅负责本次负载；调用方负责被测应用、数据恢复、资源采样和独立重复。
func Run(ctx context.Context, configuration Config) (Report, error) {
	if ctx == nil {
		return Report{}, errors.New("运行上下文不能为空")
	}
	if err := configuration.Validate(); err != nil {
		return Report{}, err
	}
	requests := newWorkload(configuration, time.Now())
	statistics := newStatistics(configuration, time.Time{})
	for _, scenario := range []string{"catalog", "detail", "order", "ticket"} {
		if configuration.Scenario == "mixed" || configuration.Scenario == scenario {
			statistics.scenarios[scenario] = newScenarioStatistics(configuration)
		}
	}
	workers := configuration.Concurrency
	if configuration.Rate > 0 {
		workers = configuration.MaxInflight
	}
	transport := &http.Transport{
		Proxy: nil, DialContext: dialLoopback, DisableCompression: true, ForceAttemptHTTP2: false,
		MaxIdleConns: workers, MaxIdleConnsPerHost: workers, MaxConnsPerHost: workers,
		IdleConnTimeout: tcpKeepAlive, ResponseHeaderTimeout: configuration.Timeout, MaxResponseHeaderBytes: maximumHeaderBytes,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: configuration.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// 有界结果通道使负载端内存不随请求总数增长；处理不及时会体现在调度滞后和丢弃中。
	events := make(chan observation, workers*2)
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for event := range events {
			statistics.add(event)
		}
	}()
	var window time.Duration
	var expected uint64
	if configuration.Rate > 0 {
		expected = plannedRequestCount(configuration)
		window = runOpen(ctx, configuration, requests, client, statistics, events, expected)
	} else {
		window = runClosed(ctx, configuration, requests, client, statistics, events)
	}
	finished := time.Now()
	close(events)
	<-collected
	report := statistics.finish(finished, window, ctx.Err() != nil, expected)
	return report, ctx.Err()
}

// plannedRequestCount 用有理数计算半开窗口内的到达数，避免 100×0.07 被舍入到 7 以上。
// 速率采用与配置 JSON 相同的最短十进制表达；请求按 i/r 在 [0,duration) 内调度。
func plannedRequestCount(configuration Config) uint64 {
	rate, _ := new(big.Rat).SetString(strconv.FormatFloat(configuration.Rate, 'f', -1, 64))
	count := rate.Mul(rate, new(big.Rat).SetFrac64(int64(configuration.Duration), int64(time.Second)))
	whole, remainder := new(big.Int).QuoRem(count.Num(), count.Denom(), new(big.Int))
	if remainder.Sign() > 0 {
		whole.Add(whole, big.NewInt(1))
	}
	return whole.Uint64()
}

func runOpen(ctx context.Context, configuration Config, requests *workload, client *http.Client, statistics *statistics, events chan<- observation, expected uint64) time.Duration {
	jobs := make(chan scheduledRequest, configuration.MaxInflight)
	slots := make(chan struct{}, configuration.MaxInflight)
	var workers, ready sync.WaitGroup
	workers.Add(configuration.MaxInflight)
	ready.Add(configuration.MaxInflight)
	for range configuration.MaxInflight {
		go func() {
			defer workers.Done()
			ready.Done()
			for job := range jobs {
				now := time.Now()
				if ctx.Err() != nil || !now.Before(job.End) {
					scenario, _ := requests.scenario(job.Sequence)
					reason := "dispatch_window_elapsed"
					if ctx.Err() != nil {
						reason = "context_canceled"
					}
					events <- observation{Sequence: job.Sequence, Scenario: scenario, Scheduled: job.When, Finished: now, Outcome: "dropped", Reason: reason}
				} else {
					events <- perform(ctx, configuration, requests, client, job)
				}
				<-slots
			}
		}()
	}
	ready.Wait()
	start := time.Now()
	statistics.start = start
	end := start.Add(configuration.Duration)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for sequence := uint64(0); sequence < expected; sequence++ {
		when := start.Add(time.Duration(float64(sequence) / configuration.Rate * float64(time.Second)))
		if !waitUntil(ctx, timer, when) {
			break
		}
		now := time.Now()
		scenario, _ := requests.scenario(sequence)
		if !now.Before(end) {
			events <- observation{Sequence: sequence, Scenario: scenario, Scheduled: when, Finished: now, Outcome: "dropped", Reason: "schedule_window_elapsed"}
			continue
		}
		select {
		case slots <- struct{}{}:
			jobs <- scheduledRequest{Sequence: sequence, When: when, End: end}
		default:
			events <- observation{Sequence: sequence, Scenario: scenario, Scheduled: when, Finished: now, Outcome: "dropped", Reason: "inflight_limit"}
		}
	}
	waitUntil(ctx, timer, end)
	window := min(time.Since(start), configuration.Duration)
	statistics.windowEnd.Store(int64(window))
	close(jobs)
	workers.Wait()
	return window
}

func runClosed(ctx context.Context, configuration Config, requests *workload, client *http.Client, statistics *statistics, events chan<- observation) time.Duration {
	var workers, ready sync.WaitGroup
	var sequence atomic.Uint64
	gate := make(chan struct{})
	workers.Add(configuration.Concurrency)
	ready.Add(configuration.Concurrency)
	for range configuration.Concurrency {
		go func() {
			defer workers.Done()
			ready.Done()
			<-gate
			end := statistics.start.Add(configuration.Duration)
			for {
				now := time.Now()
				if ctx.Err() != nil || !now.Before(end) {
					return
				}
				job := scheduledRequest{Sequence: sequence.Add(1) - 1, When: now, End: end}
				events <- perform(ctx, configuration, requests, client, job)
			}
		}()
	}
	ready.Wait()
	statistics.start = time.Now()
	close(gate)
	timer := time.NewTimer(configuration.Duration)
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
	timer.Stop()
	window := min(time.Since(statistics.start), configuration.Duration)
	statistics.windowEnd.Store(int64(window))
	workers.Wait()
	return window
}

func waitUntil(ctx context.Context, timer *time.Timer, instant time.Time) bool {
	if ctx.Err() != nil {
		return false
	}
	if delay := time.Until(instant); delay > 0 {
		timer.Reset(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	return ctx.Err() == nil
}

func perform(ctx context.Context, configuration Config, requests *workload, client *http.Client, job scheduledRequest) observation {
	specification := requests.request(job.Sequence)
	event := observation{Sequence: job.Sequence, Scenario: specification.Scenario, Scheduled: job.When}
	request, err := http.NewRequestWithContext(ctx, specification.Method, strings.TrimSuffix(configuration.URL, "/")+specification.Path, bytes.NewReader(specification.Body))
	if err != nil {
		event.Started, event.Finished = time.Now(), time.Now()
		event.Outcome, event.Reason = "transport", "request_construction"
		return event
	}
	request.Header.Set("Authorization", specification.Token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "thinkgo-business-load/1")
	if len(specification.Body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if specification.IdempotencyKey != "" {
		request.Header.Set("Idempotency-Key", specification.IdempotencyKey)
	}
	event.Started = time.Now()
	response, err := client.Do(request)
	if err != nil {
		event.Finished, event.Outcome, event.Reason = time.Now(), "transport", transportReason(err)
		return event
	}
	event.Status = response.StatusCode
	if specification.Scenario == "detail" {
		cache := response.Header.Get("X-Benchmark-Cache")
		if cache == "hit" || cache == "miss" {
			event.Cache = cache
		}
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	event.Finished = time.Now()
	_ = response.Body.Close()
	if readErr != nil {
		event.Outcome, event.Reason = "transport", transportReason(readErr)
		return event
	}
	if response.StatusCode != expectedStatus(specification.Scenario) {
		event.Outcome, event.Reason = "status", errResponseStatus.Error()
		return event
	}
	if len(body) > maximumResponseBytes {
		event.Outcome, event.Reason = "semantic", errResponseTooLong.Error()
		return event
	}
	if err := validateResponse(configuration, specification, response.StatusCode, response.Header, body); err != nil {
		event.Outcome, event.Reason = "semantic", err.Error()
		return event
	}
	event.Outcome = "success"
	return event
}

func transportReason(err error) string {
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
		return "request_timeout"
	}
	return "connection_or_body_error"
}
