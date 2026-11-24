package cleanup

import (
	"errors"
	"os"
	"runtime"
	"testing"
)

// 两种运行时模式都只隔离真正的 panic，保留回调结果、处理器自身的异常和协程退出。
func TestCatchPreservesCallbackAndHandlerOutcomes(t *testing.T) {
	for _, mode := range []string{"0", "1"} {
		t.Run("panicnil="+mode, func(t *testing.T) {
			t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",panicnil="+mode)
			marker := errors.New("原始错误")
			for _, outcome := range []string{"return", "panic", "nil_panic", "goexit", "handler_panic"} {
				t.Run(outcome, func(t *testing.T) {
					done := make(chan struct{})
					var recovered any
					var handled, returned bool
					go func() {
						defer close(done)
						defer func() { recovered = recover() }()
						result := Catch(func() error {
							switch outcome {
							case "panic", "handler_panic":
								panic(marker)
							case "nil_panic":
								panic(nil)
							case "goexit":
								runtime.Goexit()
							}
							return marker
						}, func(value any) error {
							handled = true
							switch outcome {
							case "handler_panic":
								panic(marker)
							case "panic":
								if value != marker {
									t.Error("改变了原始 panic")
								}
							case "nil_panic":
								if (value == nil) != (mode == "1") {
									t.Errorf("错误的 nil panic 值：%v", value)
								}
							}
							return marker
						})
						returned = true
						if result != marker {
							t.Errorf("回调或处理器的结果丢失：%v", result)
						}
					}()
					<-done
					if handled != (outcome != "return" && outcome != "goexit") ||
						returned != (outcome != "goexit" && outcome != "handler_panic") ||
						(outcome == "handler_panic" && recovered != marker) || (outcome != "handler_panic" && recovered != nil) {
						t.Fatalf("退出语义改变：处理=%t，返回=%t，异常=%v", handled, returned, recovered)
					}
				})
			}
		})
	}
}

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
