package driver

import (
	"testing"
	"time"
)

// TestFileOperationsShareAtomicGuard 验证普通写入、读取、删除与计数使用原子更新的同一跨进程边界。
func TestFileOperationsShareAtomicGuard(t *testing.T) {
	for _, operation := range []string{"get", "set", "delete", "counter"} {
		t.Run(operation, func(t *testing.T) {
			first, directory := newTestFileDriver(t)
			second, err := NewFile(directory)
			if err != nil {
				t.Fatal(err)
			}
			if err := first.Set("value", 0, 0); err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}), make(chan struct{})
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			go func() {
				firstDone <- first.Update("value", 0, func(interface{}, bool) (interface{}, bool, error) {
					close(started)
					<-release
					return 1, false, nil
				})
			}()
			<-started
			var observed interface{}
			go func() {
				switch operation {
				case "get":
					value, _, err := second.Get("value")
					observed = value
					secondDone <- err
				case "set":
					secondDone <- second.Set("value", 2, 0)
				case "delete":
					secondDone <- second.Delete("value")
				case "counter":
					_, err := second.Inc("value", 1)
					secondDone <- err
				}
			}()
			early := false
			var secondErr error
			select {
			case secondErr = <-secondDone:
				early = true
			case <-time.After(40 * time.Millisecond):
			}
			close(release)
			firstErr := <-firstDone
			if !early {
				secondErr = <-secondDone
			}
			if early || firstErr != nil || secondErr != nil {
				t.Fatalf("操作绕过稳定锁: early=%t first=%v second=%v", early, firstErr, secondErr)
			}
			value, found, err := first.Get("value")
			if err != nil {
				t.Fatal(err)
			}
			if operation == "get" && observed != float64(1) || operation == "delete" && found ||
				(operation == "set" || operation == "counter") && (!found || value != float64(2)) {
				t.Fatalf("原子更新之后的操作丢失: observed=%v current=%v found=%t", observed, value, found)
			}
		})
	}
}
