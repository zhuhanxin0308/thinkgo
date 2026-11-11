package env

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
)

// TestProcessSnapshotsPreserveCapturedOrdering 验证相关进程键来自同一 Environ 快照。
func TestProcessSnapshotsPreserveCapturedOrdering(t *testing.T) {
	const first = "THINKGO_ATOMIC_SNAPSHOT_FIRST"
	const second = "THINKGO_ATOMIC_SNAPSHOT_SECOND"
	const paddingKeys = 128
	const samples = 1000
	t.Setenv(first, "0")
	for index := range paddingKeys {
		t.Setenv(fmt.Sprintf("THINKGO_ATOMIC_SNAPSHOT_PADDING_%d", index), "fixture")
	}
	t.Setenv(second, "0")
	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Go(func() {
		for value := 1; ; value++ {
			select {
			case <-stop:
				return
			default:
			}
			encoded := strconv.Itoa(value)
			// FIRST 总在 SECOND 之前递增，任何真实进程状态都应满足 FIRST >= SECOND。
			if os.Setenv(first, encoded) != nil || os.Setenv(second, encoded) != nil {
				return
			}
		}
	})
	defer func() { close(stop); writer.Wait() }()
	environment := NewEnv()
	for index := range samples {
		var values map[string]string
		if index%2 == 0 {
			values = environment.Snapshot().All()
		} else {
			values = environment.All()
		}
		a, err := strconv.Atoi(values[first])
		if err != nil {
			t.Fatal(err)
		}
		b, err := strconv.Atoi(values[second])
		if err != nil {
			t.Fatal(err)
		}
		if a < b {
			t.Fatalf("快照混用了不同时刻的值: FIRST=%d SECOND=%d", a, b)
		}
	}
}
