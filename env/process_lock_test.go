package env

import (
	"testing"
	"time"
)

func TestLiveProcessReadsDoNotBlockInstallingAFrozenEnvironment(t *testing.T) {
	tests := []struct {
		name string
		read func(*Env, func()) string
		want string
	}{
		{name: "单键", want: "frozen-file", read: func(environment *Env, block func()) string {
			value, _ := environment.lookup("DATA", func(string) (string, bool) {
				block()
				return "live", true
			})
			return value
		}},
		{name: "快照", want: "frozen-file/frozen-process", read: func(environment *Env, block func()) string {
			file, process := environment.readEnvironmentValues(func() map[string]string {
				block()
				return map[string]string{"PROCESS": "live"}
			})
			return file["DATA"] + "/" + process["PROCESS"]
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := NewEnv()
			frozen := &Env{
				data:            map[string]string{"DATA": "frozen-file"},
				processSnapshot: map[string]string{"PROCESS": "frozen-process"},
			}
			started, release := make(chan struct{}), make(chan struct{})
			received := make(chan string, 1)
			go func() {
				received <- test.read(environment, func() {
					close(started)
					<-release
				})
			}()
			<-started
			replaced := make(chan struct{})
			go func() {
				environment.ReplaceWithSnapshot(frozen)
				close(replaced)
			}()
			select {
			case <-replaced:
			case <-time.After(time.Second):
				t.Error("进程环境读取期间写者仍在等待 Env 的锁")
			}
			close(release)
			if result := <-received; result != test.want {
				t.Errorf("应使用已安装的冻结环境，得到 %q", result)
			}
			<-replaced
		})
	}
}
