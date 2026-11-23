package cache

import (
	"context"
	"errors"
	"sync"
)

type rememberCall struct {
	done  chan struct{}
	value interface{}
	err   error
	// 仅由执行回调的协程读写，业务错误优先于该调用者自己的取消。
	callbackFailed bool

	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	// 同一取消通道上的调用总是同时失效；nil 通道代表不会取消的订阅。
	subscribers map[<-chan struct{}]struct{}
}

// hasSubscribersLocked 在接纳新调用和进入后端前同步核实取消，不能仅靠异步通知。
func (call *rememberCall) hasSubscribersLocked() bool {
	for done := range call.subscribers {
		select {
		case <-done:
			delete(call.subscribers, done)
		default:
			return true
		}
	}
	call.cancel()
	return false
}

func (call *rememberCall) activeContext() context.Context {
	call.mu.Lock()
	defer call.mu.Unlock()
	call.hasSubscribersLocked()
	return call.ctx
}

func (call *rememberCall) subscribe(done <-chan struct{}) bool {
	call.mu.Lock()
	defer call.mu.Unlock()
	if !call.hasSubscribersLocked() {
		return false
	}
	call.subscribers[done] = struct{}{}
	return true
}

func (call *rememberCall) unsubscribe(done <-chan struct{}) {
	call.mu.Lock()
	defer call.mu.Unlock()
	delete(call.subscribers, done)
	if len(call.subscribers) == 0 {
		call.cancel()
	}
}

func (s *cacheState) doRememberContext(ctx context.Context, key string, load func(*rememberCall) (interface{}, error)) (interface{}, error) {
	done := ctx.Done()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.rememberMu.Lock()
		call := s.remembering[key]
		if call == nil {
			shared, cancel := context.WithCancel(context.WithoutCancel(ctx))
			call = &rememberCall{
				done: make(chan struct{}), ctx: shared, cancel: cancel,
				subscribers: map[<-chan struct{}]struct{}{done: {}},
			}
			s.remembering[key] = call
			s.rememberMu.Unlock()
			return s.runRemember(ctx, key, call, load)
		}
		joined := call.subscribe(done)
		s.rememberMu.Unlock()
		select {
		case <-done:
			if joined {
				call.unsubscribe(done)
			}
			return nil, ctx.Err()
		case <-call.done:
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if joined {
				return call.value, call.err
			}
			// 此前所有订阅均已取消；旧回调完全退出后才能重新竞争同一键。
		}
	}
}

func (s *cacheState) runRemember(ctx context.Context, key string, call *rememberCall, load func(*rememberCall) (interface{}, error)) (value interface{}, err error) {
	if done := ctx.Done(); done != nil {
		// 首个调用在自身协程执行回调，后端阻塞时仍需响应最后一个订阅者取消。
		stop := context.AfterFunc(ctx, func() { call.unsubscribe(done) })
		defer stop()
	}
	defer call.cancel()
	returned := false
	defer func() {
		recovered := recover()
		if recovered != nil {
			value, err = nil, errors.New("Remember 回调发生 panic")
		} else if !returned {
			// Goexit 必须唤醒等待者，并释放此键以供后续加载。
			value, err = nil, errors.New("Remember 回调异常退出")
		}
		s.rememberMu.Lock()
		call.value, call.err = value, err
		delete(s.remembering, key)
		close(call.done)
		s.rememberMu.Unlock()
		if recovered != nil {
			panic(recovered)
		}
		if ctxErr := ctx.Err(); ctxErr != nil && !call.callbackFailed {
			value, err = nil, ctxErr
		}
	}()
	value, err = load(call)
	returned = true
	return value, err
}
