package event

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/cleanup"
)

const (
	terminalListenerTimeout = 40 * time.Millisecond
	terminalListenerWait    = 2 * time.Second
)

type terminalContextListener struct {
	handle func(context.Context, Event) error
}

func (listener *terminalContextListener) Handle(Event) error {
	return nil
}

func (listener *terminalContextListener) HandleContext(ctx context.Context, currentEvent Event) error {
	return listener.handle(ctx, currentEvent)
}

// 终止监听器即使连续异常退出，也要在原协程按序收尾，监督入口须在最后一项完成后发布结果。
func TestTerminalDispatchContinuesAfterGoexit(t *testing.T) {
	for _, mode := range []string{"serial", "supervised"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				dispatcher := NewDispatcher()
				firstErr, lastErr := errors.New("先前错误"), errors.New("末项错误")
				releaseLast := make(chan struct{})
				var order []string
				for _, name := range []string{"先前错误", "退出", "panic", "再次退出", "末项错误"} {
					if err := dispatcher.Listen(EventHttpEnd, &SimpleListener{Handler: func(Event) error {
						order = append(order, name)
						switch name {
						case "先前错误":
							return firstErr
						case "退出", "再次退出":
							defer func() { order = append(order, "回调已收尾") }()
							runtime.Goexit()
						case "panic":
							panic("监听器 panic")
						case "末项错误":
							<-releaseLast
							return lastErr
						}
						return nil
					}}); err != nil {
						t.Fatal(err)
					}
				}

				var result error
				returned := false
				finished := make(chan struct{})
				go func() {
					defer close(finished)
					end := NewHttpEndEvent(http.StatusOK)
					if mode == "serial" {
						result = dispatcher.DispatchTerminalSerialContext(ctx, end)
					} else {
						result = dispatcher.DispatchTerminalContext(ctx, end)
					}
					returned = true
				}()
				synctest.Wait()
				select {
				case <-finished:
					t.Error("最后一个监听器仍在等待，终止分发却已结束")
				default:
				}
				close(releaseLast)
				synctest.Wait()
				select {
				case <-finished:
				default:
					t.Fatal("所有监听器均已解除阻塞，终止分发仍未结束")
				}
				want := []string{"先前错误", "退出", "回调已收尾", "panic", "再次退出", "回调已收尾", "末项错误"}
				if !slices.Equal(order, want) {
					t.Errorf("监听器执行顺序为 %v，期望 %v", order, want)
				}
				if returned != (mode == "supervised") {
					t.Errorf("原调用协程的退出语义改变：returned=%t", returned)
				}
				if mode == "supervised" && (!errors.Is(result, cleanup.ErrAborted) || !errors.Is(result, ErrEventCallbackPanic) ||
					!errors.Is(result, firstErr) || !errors.Is(result, lastErr)) {
					t.Errorf("监督结果缺少监听器的失败原因：%v", result)
				}
			})
		})
	}
}

// 串行监督者在原协程退出后也能收到本次分发的错误，监听器只接收原上下文。
func TestTerminalSerialPublishesOwnResultAfterListenersFinish(t *testing.T) {
	type contextKey struct{}
	for _, exits := range []bool{false, true} {
		mode := "return"
		if exits {
			mode = "goexit"
		}
		t.Run(mode, func(t *testing.T) {
			original, cancel := context.WithTimeout(context.WithValue(t.Context(), contextKey{}, mode), terminalListenerWait)
			defer cancel()
			deadline, _ := original.Deadline()
			firstErr, lastErr := errors.New("首个监听器失败"), errors.New("末个监听器失败")
			nestedErr := errors.New("嵌套事件失败")
			unpublished := errors.New("尚未发布")
			published := unpublished
			dispatcher := NewDispatcher()
			if err := dispatcher.Listen("nested", &SimpleListener{Handler: func(Event) error { return nestedErr }}); err != nil {
				t.Fatal(err)
			}
			if err := dispatcher.Listen(EventHttpEnd, &SimpleListener{Handler: func(Event) error { return firstErr }}); err != nil {
				t.Fatal(err)
			}
			if err := dispatcher.Listen(EventHttpEnd, &terminalContextListener{handle: func(ctx context.Context, _ Event) error {
				receivedDeadline, hasDeadline := ctx.Deadline()
				if ctx != original || ctx.Value(contextKey{}) != mode || !hasDeadline || !receivedDeadline.Equal(deadline) {
					t.Error("终止监听器没有收到原始上下文")
				}
				err := dispatcher.DispatchTerminalSerialContext(ctx, NewEvent("nested", nil))
				if !errors.Is(err, nestedErr) || published != unpublished {
					t.Errorf("嵌套分发改写了外层结果：nested=%v，outer=%v", err, published)
				}
				if exits {
					runtime.Goexit()
				}
				return nil
			}}); err != nil {
				t.Fatal(err)
			}
			if err := dispatcher.Listen(EventHttpEnd, &SimpleListener{Handler: func(Event) error {
				if published != unpublished {
					t.Error("末个监听器运行前已发布分发结果")
				}
				return lastErr
			}}); err != nil {
				t.Fatal(err)
			}

			finished := make(chan struct{})
			var returned error
			go func() {
				defer close(finished)
				ctx := cleanup.WithTerminalResult(original, &published)
				returned = dispatcher.DispatchTerminalSerialContext(ctx, NewHttpEndEvent(http.StatusOK))
			}()
			<-finished
			if !errors.Is(published, firstErr) || !errors.Is(published, lastErr) || errors.Is(published, nestedErr) ||
				errors.Is(published, cleanup.ErrAborted) != exits {
				t.Errorf("监督者未获得本次分发的完整结果：%v", published)
			}
			if exits && returned != nil || !exits && returned != published {
				t.Errorf("分发返回的结果错误：%v", returned)
			}
		})
	}
}

// TestDispatcherDispatchTerminalContextPreservesSequentialOrderAfterDeadline 验证不感知
// 上下文的旧监听器阻塞时，调用方仍按截止返回，但监督任务必须等待它真实结束后
// 才能启动后续监听器，避免多个监听器重叠访问同一个 Event。
func TestDispatcherDispatchTerminalContextPreservesSequentialOrderAfterDeadline(t *testing.T) {
	dispatcher := NewDispatcher()
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstReturned := make(chan struct{})
	defer func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
		select {
		case <-firstReturned:
		case <-time.After(terminalListenerWait):
			t.Error("阻塞的旧监听器释放后仍未返回")
		}
	}()

	if err := dispatcher.ListenPriority("terminal.blocking", &SimpleListener{Handler: func(Event) error {
		close(firstStarted)
		<-releaseFirst
		close(firstReturned)
		return nil
	}}, 10); err != nil {
		t.Fatalf("注册阻塞监听器失败: %v", err)
	}
	if err := dispatcher.ListenPriority("terminal.blocking", &SimpleListener{Handler: func(Event) error {
		close(secondStarted)
		return nil
	}}, 1); err != nil {
		t.Fatalf("注册后续监听器失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), terminalListenerTimeout)
	defer cancel()
	startedAt := time.Now()
	err := dispatcher.DispatchTerminalContext(ctx, NewEvent("terminal.blocking", nil))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("阻塞监听器应使终止分发返回截止错误，实际为 %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= terminalListenerWait {
		t.Fatalf("终止分发没有保持有界，耗时 %s", elapsed)
	}
	select {
	case <-firstStarted:
	default:
		t.Fatal("首个监听器未启动")
	}
	select {
	case <-secondStarted:
		t.Fatal("首个监听器真实返回前不得启动后续监听器")
	default:
	}
	close(releaseFirst)
	select {
	case <-firstReturned:
	case <-time.After(terminalListenerWait):
		t.Fatal("释放首个监听器后仍未返回")
	}
	select {
	case <-secondStarted:
	case <-time.After(terminalListenerWait):
		t.Fatal("首个监听器返回后，监督任务未继续启动后续监听器")
	}
}

// TestDispatcherDispatchTerminalSerialContextAggregatesFailures 验证同步串行原语会聚合
// panic、普通错误和上下文错误，且任何一种失败都不会跳过后续收尾监听器。
func TestDispatcherDispatchTerminalContextAggregatesFailures(t *testing.T) {
	dispatcher := NewDispatcher()
	listenerErr := errors.New("终止监听器失败")
	lastStarted := make(chan struct{})

	if err := dispatcher.ListenPriority("terminal.failures", &SimpleListener{Handler: func(Event) error {
		panic("终止监听器 panic")
	}}, 40); err != nil {
		t.Fatalf("注册 panic 监听器失败: %v", err)
	}
	if err := dispatcher.ListenPriority("terminal.failures", &SimpleListener{Handler: func(Event) error {
		return listenerErr
	}}, 30); err != nil {
		t.Fatalf("注册错误监听器失败: %v", err)
	}
	if err := dispatcher.ListenPriority("terminal.failures", &terminalContextListener{handle: func(ctx context.Context, _ Event) error {
		<-ctx.Done()
		return ctx.Err()
	}}, 20); err != nil {
		t.Fatalf("注册超时监听器失败: %v", err)
	}
	if err := dispatcher.ListenPriority("terminal.failures", &SimpleListener{Handler: func(Event) error {
		close(lastStarted)
		return nil
	}}, 10); err != nil {
		t.Fatalf("注册最后监听器失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := dispatcher.DispatchTerminalSerialContext(ctx, NewEvent("terminal.failures", nil))
	if !errors.Is(err, ErrEventCallbackPanic) {
		t.Fatalf("聚合错误缺少 panic，实际为 %v", err)
	}
	if !errors.Is(err, listenerErr) {
		t.Fatalf("聚合错误缺少监听器错误，实际为 %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("聚合错误缺少上下文取消错误，实际为 %v", err)
	}
	select {
	case <-lastStarted:
	case <-time.After(terminalListenerWait):
		t.Fatal("panic、错误或超时后，最后监听器仍未启动")
	}
}

// TestDispatcherDispatchTerminalContextUsesPriorityOrder 验证终止监听器仍按数值优先级
// 和注册顺序依次启动。
func TestDispatcherDispatchTerminalContextUsesPriorityOrder(t *testing.T) {
	dispatcher := NewDispatcher()
	var orderLock sync.Mutex
	order := make([]string, 0, 3)
	register := func(priority int, name string) {
		t.Helper()
		if err := dispatcher.ListenPriority("terminal.order", &SimpleListener{Handler: func(Event) error {
			orderLock.Lock()
			order = append(order, name)
			orderLock.Unlock()
			return nil
		}}, priority); err != nil {
			t.Fatalf("注册 %s 监听器失败: %v", name, err)
		}
	}
	register(1, "low")
	register(20, "high-first")
	register(20, "high-second")

	if err := dispatcher.DispatchTerminalContext(context.Background(), NewEvent("terminal.order", nil)); err != nil {
		t.Fatalf("按优先级终止分发失败: %v", err)
	}
	orderLock.Lock()
	defer orderLock.Unlock()
	want := []string{"high-first", "high-second", "low"}
	if len(order) != len(want) {
		t.Fatalf("监听器启动数量不正确，实际为 %#v", order)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("监听器启动顺序不正确，实际为 %#v", order)
		}
	}
}

// TestDispatcherDispatchTerminalContextIgnoresStopPropagation 验证及时和晚到的
// StopPropagation 都不会破坏“尝试全部收尾监听器”的终止分发语义。
func TestDispatcherDispatchTerminalContextIgnoresStopPropagation(t *testing.T) {
	t.Run("及时停止传播", func(t *testing.T) {
		dispatcher := NewDispatcher()
		var followerCalled atomic.Int32
		if err := dispatcher.ListenPriority("terminal.stop.now", &SimpleListener{Handler: func(currentEvent Event) error {
			currentEvent.(*SimpleEvent).StopPropagation()
			return nil
		}}, 10); err != nil {
			t.Fatalf("注册停止传播监听器失败: %v", err)
		}
		if err := dispatcher.ListenPriority("terminal.stop.now", &SimpleListener{Handler: func(Event) error {
			followerCalled.Add(1)
			return nil
		}}, 1); err != nil {
			t.Fatalf("注册后续监听器失败: %v", err)
		}

		if err := dispatcher.DispatchTerminalContext(context.Background(), NewEvent("terminal.stop.now", nil)); err != nil {
			t.Fatalf("及时停止传播的终止分发失败: %v", err)
		}
		if followerCalled.Load() != 1 {
			t.Fatalf("StopPropagation 不应跳过收尾监听器，实际执行 %d 次", followerCalled.Load())
		}
	})

	t.Run("晚停止传播", func(t *testing.T) {
		dispatcher := NewDispatcher()
		releaseStopper := make(chan struct{})
		stopperReturned := make(chan struct{})
		followerStarted := make(chan struct{})
		defer func() {
			select {
			case <-releaseStopper:
			default:
				close(releaseStopper)
			}
			select {
			case <-stopperReturned:
			case <-time.After(terminalListenerWait):
				t.Error("晚停止传播监听器释放后仍未返回")
			}
		}()

		if err := dispatcher.ListenPriority("terminal.stop.late", &SimpleListener{Handler: func(currentEvent Event) error {
			<-releaseStopper
			currentEvent.(*SimpleEvent).StopPropagation()
			close(stopperReturned)
			return nil
		}}, 10); err != nil {
			t.Fatalf("注册晚停止传播监听器失败: %v", err)
		}
		if err := dispatcher.ListenPriority("terminal.stop.late", &SimpleListener{Handler: func(Event) error {
			close(followerStarted)
			return nil
		}}, 1); err != nil {
			t.Fatalf("注册后续监听器失败: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), terminalListenerTimeout)
		defer cancel()
		currentEvent := NewEvent("terminal.stop.late", nil)
		err := dispatcher.DispatchTerminalContext(ctx, currentEvent)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("晚停止传播监听器应先触发截止，实际为 %v", err)
		}
		select {
		case <-followerStarted:
			t.Fatal("晚 StopPropagation 监听器返回前不得启动后续监听器")
		default:
		}
		close(releaseStopper)
		select {
		case <-stopperReturned:
		case <-time.After(terminalListenerWait):
			t.Fatal("晚停止传播监听器未返回")
		}
		select {
		case <-followerStarted:
		case <-time.After(terminalListenerWait):
			t.Fatal("晚 StopPropagation 监听器返回后未继续分发")
		}
		if !currentEvent.IsPropagationStopped() {
			t.Fatal("监听器晚返回后应仍可更新事件自身状态")
		}
	})
}

// TestDispatcherDispatchTerminalContextPropagatesDeadline 验证上下文监听器会收到
// 同一个截止信号，并能主动结束自己的 goroutine。
func TestDispatcherDispatchTerminalContextPropagatesDeadline(t *testing.T) {
	dispatcher := NewDispatcher()
	contextSeen := make(chan error, 1)
	listener := &terminalContextListener{handle: func(ctx context.Context, _ Event) error {
		<-ctx.Done()
		contextSeen <- ctx.Err()
		return ctx.Err()
	}}
	if err := dispatcher.Listen("terminal.context", listener); err != nil {
		t.Fatalf("注册上下文监听器失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), terminalListenerTimeout)
	defer cancel()
	err := dispatcher.DispatchTerminalContext(ctx, NewEvent("terminal.context", nil))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("终止分发应返回上下文截止错误，实际为 %v", err)
	}
	select {
	case seenErr := <-contextSeen:
		if !errors.Is(seenErr, context.DeadlineExceeded) {
			t.Fatalf("上下文监听器收到的错误不正确: %v", seenErr)
		}
	case <-time.After(terminalListenerWait):
		t.Fatal("上下文监听器未感知截止信号")
	}
}

// TestDispatcherDispatchTerminalContextRejectsInvalidInput 验证终止分发沿用普通分发
// 的输入校验，不接受 nil 上下文或空事件。
func TestDispatcherDispatchTerminalContextRejectsInvalidInput(t *testing.T) {
	dispatcher := NewDispatcher()
	var nilContext context.Context
	if err := dispatcher.DispatchTerminalContext(nilContext, NewEvent("terminal.nil-context", nil)); !errors.Is(err, ErrInvalidEventContext) {
		t.Fatalf("nil 上下文应返回 ErrInvalidEventContext，实际为 %v", err)
	}
	if err := dispatcher.DispatchTerminalContext(context.Background(), nil); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("空事件应返回 ErrInvalidEvent，实际为 %v", err)
	}
}
