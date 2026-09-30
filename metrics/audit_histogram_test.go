package metrics

import (
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuditErrorsCountOnly5xx(t *testing.T) {
	registry := NewRegistry()
	registry.Enable()
	for _, status := range []int{-1, 0, 99, 100, 200, 499, 500, 599, 600, 999} {
		registry.Observe(status, time.Millisecond)
	}
	snapshot := registry.Snapshot()
	if snapshot.Requests != 10 || snapshot.Errors != 2 || snapshot.StatusClasses[4] != 2 || snapshot.OtherStatuses != 5 {
		t.Fatalf("incorrect status classification: %#v", snapshot)
	}
}

func TestAuditHistogramDistribution(t *testing.T) {
	registry := NewRegistry()
	registry.Enable()
	durations := []time.Duration{-time.Second, 0, time.Millisecond, time.Millisecond + time.Nanosecond, 5 * time.Millisecond, 10 * time.Second, 11 * time.Second}
	for _, duration := range durations {
		registry.Observe(http.StatusOK, duration)
	}
	snapshot := registry.Snapshot()
	for index, bound := range durationBucketBounds {
		var expected uint64
		for _, duration := range durations {
			if duration.Seconds() <= bound {
				expected++
			}
		}
		if snapshot.DurationBuckets[index] != expected {
			t.Fatalf("bucket %g: got %d want %d", bound, snapshot.DurationBuckets[index], expected)
		}
	}
	if snapshot.DurationCount != uint64(len(durations)) || snapshot.DurationBuckets[bucketCount-1] != uint64(len(durations)) {
		t.Fatalf("incorrect histogram total: %#v", snapshot)
	}
}

func TestAuditHistogramConcurrentSnapshots(t *testing.T) {
	registry := NewRegistry()
	registry.Enable()
	const workers = 4
	start := make(chan struct{})
	stop := make(chan struct{})
	var group sync.WaitGroup
	group.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer group.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
					registry.Observe(http.StatusOK, time.Microsecond)
				}
			}
		}()
	}
	defer func() {
		close(stop)
		group.Wait()
	}()
	close(start)
	for index := 0; index < 5000; index++ {
		snapshot := registry.Snapshot()
		if err := auditHistogramInvariant(snapshot); err != nil {
			t.Fatal(err)
		}
		if index%100 == 0 {
			if err := auditPrometheusHistogramInvariant(registry.Prometheus()); err != nil {
				t.Fatal(err)
			}
			runtime.Gosched()
		}
	}
}

func TestAuditHistogramConcurrentReset(t *testing.T) {
	registry := NewRegistry()
	registry.Enable()
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		for index := 0; index < 1000; index++ {
			registry.Observe(http.StatusOK, time.Duration(index)*time.Millisecond)
			registry.Reset()
		}
	}()
	defer group.Wait()
	for index := 0; index < 1000; index++ {
		if err := auditHistogramInvariant(registry.Snapshot()); err != nil {
			t.Fatal(err)
		}
	}
}

func auditHistogramInvariant(snapshot Snapshot) error {
	var previous uint64
	for index, count := range snapshot.DurationBuckets {
		if count < previous {
			return fmt.Errorf("histogram is not cumulative at bucket %d: %v", index, snapshot.DurationBuckets)
		}
		previous = count
	}
	if snapshot.DurationCount != previous {
		return fmt.Errorf("histogram count %d differs from +Inf bucket %d", snapshot.DurationCount, previous)
	}
	return nil
}

func auditPrometheusHistogramInvariant(output string) error {
	var infinity, count uint64
	var hasInfinity, hasCount bool
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case `thinkgo_http_request_duration_seconds_bucket{le="+Inf"}`:
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return err
			}
			infinity, hasInfinity = value, true
		case "thinkgo_http_request_duration_seconds_count":
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return err
			}
			count, hasCount = value, true
		}
	}
	if !hasInfinity || !hasCount || infinity != count {
		return fmt.Errorf("invalid exposition: +Inf=%d (present=%t), count=%d (present=%t)", infinity, hasInfinity, count, hasCount)
	}
	return nil
}

func BenchmarkAuditObserve(b *testing.B) {
	for _, duration := range []time.Duration{time.Microsecond, 50 * time.Millisecond, 11 * time.Second} {
		b.Run(duration.String(), func(b *testing.B) {
			registry := NewRegistry()
			registry.Enable()
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				registry.Observe(http.StatusOK, duration)
			}
		})
	}
}
