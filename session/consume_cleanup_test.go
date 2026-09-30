package session

import (
	"runtime"
	"testing"
)

type terminatingSessionDriver struct {
	Driver
	terminate func()
}

func (d *terminatingSessionDriver) Update(id string, update func(string, bool) (string, bool, error)) error {
	d.terminate()
	return d.Driver.Update(id, update)
}

// TestConsumeStringReleasesMutexOnAbnormalDriverExit avoids blocking assertions on a leaked lock.
func TestConsumeStringReleasesMutexOnAbnormalDriverExit(t *testing.T) {
	for _, mode := range []string{"panic", "goexit"} {
		t.Run(mode, func(t *testing.T) {
			manager, driver, cookie := newPersistedStringSession(t)
			request := loadStringSession(t, manager, cookie)
			marker := new(int)
			request.driver = &terminatingSessionDriver{Driver: driver, terminate: func() {
				if mode == "panic" {
					panic(marker)
				}
				runtime.Goexit()
			}}
			var recovered interface{}
			exited := make(chan struct{})
			go func() {
				defer close(exited)
				defer func() { recovered = recover() }()
				_, _ = request.ConsumeString("token", "secret")
			}()
			<-exited
			if mode == "panic" && recovered != marker {
				t.Errorf("panic identity changed: %v", recovered)
			}
			if !request.mu.TryLock() {
				t.Fatal("driver exit left the Session mutex permanently locked")
			}
			request.mu.Unlock()
			if !request.saveMu.TryLock() {
				t.Fatal("driver exit left the save mutex locked")
			}
			request.saveMu.Unlock()
			request.driver = driver
			accepted, err := request.ConsumeString("token", "secret")
			if err != nil || !accepted {
				t.Errorf("uncommitted token could not be retried: accepted=%v err=%v", accepted, err)
			}
		})
	}
}
