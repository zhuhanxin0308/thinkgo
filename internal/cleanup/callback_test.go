package cleanup

import (
	"errors"
	"os"
	"runtime"
	"testing"
)

// 错误按原身份返回，只有回调异常退出时才在拥有它的协程内执行收尾。
func TestCallPreservesCallbackOutcome(t *testing.T) {
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",panicnil=1")
	marker := errors.New("回调结果")
	for _, mode := range []string{"return", "panic", "nil_panic", "goexit"} {
		t.Run(mode, func(t *testing.T) {
			var returned, resumed, panicked bool
			var cleanupCalls int
			var recovered interface{}
			done := make(chan struct{})
			go func() {
				defer close(done)
				func() {
					defer func() { recovered = recover() }()
					err := Call(func() error {
						switch mode {
						case "panic":
							panic(marker)
						case "nil_panic":
							panic(nil)
						case "goexit":
							runtime.Goexit()
						}
						return marker
					}, func(wasPanic bool) {
						cleanupCalls++
						panicked = wasPanic
					})
					returned = true
					if err != marker {
						t.Error("普通错误的身份改变")
					}
				}()
				resumed = true
			}()
			<-done
			if returned != (mode == "return") || resumed != (mode != "goexit") {
				t.Fatalf("回调退出方式改变：返回=%t，恢复=%t", returned, resumed)
			}
			wantCleanup := 1
			if mode == "return" {
				wantCleanup = 0
			}
			if cleanupCalls != wantCleanup || panicked != (mode == "panic" || mode == "nil_panic") {
				t.Errorf("异常收尾的调用次数或原因错误：次数=%d，panic=%t", cleanupCalls, panicked)
			}
			if (mode == "panic" && recovered != marker) || (mode != "panic" && recovered != nil) {
				t.Errorf("panic 身份改变：%v", recovered)
			}
		})
	}
}
