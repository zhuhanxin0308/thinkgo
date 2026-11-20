package cache

import (
	"context"
	"errors"
	"sync"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/cachesnapshot"
)

func (s *cacheState) doRememberContext(ctx context.Context, key string, callback func(context.Context) (interface{}, error)) (value interface{}, err error) {
	if ctx == nil {
		return nil, ErrInvalidCacheContext
	}
	s.rememberMu.Lock()
	if existing := s.remembering[key]; existing != nil {
		if err := ctx.Err(); err != nil {
			s.rememberMu.Unlock()
			return nil, err
		}
		existing.waiters++
		s.rememberMu.Unlock()
		select {
		case <-existing.done:
			if existing.err != nil {
				return nil, existing.err
			}
			return cachesnapshot.Clone(existing.value)
		case <-ctx.Done():
			s.releaseRememberWaiter(key, existing, ctx.Err())
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		s.rememberMu.Unlock()
		return nil, err
	}
	sharedCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	call := &rememberCall{
		done:      make(chan struct{}),
		sharedCtx: sharedCtx,
		cancel:    cancel,
		waiters:   1,
	}
	s.remembering[key] = call
	s.rememberMu.Unlock()

	var leaderReleased sync.Once
	releaseLeader := func() {
		leaderReleased.Do(func() {
			s.releaseRememberWaiter(key, call, ctx.Err())
		})
	}
	stopWatch := context.AfterFunc(ctx, releaseLeader)
	defer func() {
		stopWatch()
		cancel(nil)
	}()
	if err := ctx.Err(); err != nil {
		releaseLeader()
		if !s.hasActiveRememberWaiters(call) {
			s.finishRemember(key, call, nil, err)
			return nil, err
		}
	}

	callbackReturned := false
	defer func() {
		recovered := recover()
		if recovered != nil {
			value, err = nil, errors.New("Remember 回调发生 panic")
		} else if !callbackReturned {
			// Goexit 没有返回值，必须通知等待者失败，不能永久保留共享加载状态。
			value, err = nil, errors.New("Remember 回调异常退出")
		}
		sharedValue := value
		if err == nil {
			value, err = cachesnapshot.Clone(sharedValue)
		}
		s.finishRemember(key, call, sharedValue, err)
		if recovered != nil {
			panic(recovered)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			value, err = nil, ctxErr
		}
	}()
	value, err = callback(sharedCtx)
	callbackReturned = true
	return value, err
}

func (s *cacheState) hasActiveRememberWaiters(call *rememberCall) bool {
	s.rememberMu.Lock()
	active := !call.finished && call.waiters > 0
	s.rememberMu.Unlock()
	return active
}

func (s *cacheState) releaseRememberWaiter(key string, call *rememberCall, cause error) {
	s.rememberMu.Lock()
	if call.finished {
		s.rememberMu.Unlock()
		return
	}
	if call.waiters > 0 {
		call.waiters--
	}
	shouldCancel := call.waiters == 0
	if shouldCancel && s.remembering[key] == call {
		delete(s.remembering, key)
	}
	s.rememberMu.Unlock()
	if shouldCancel && call.cancel != nil {
		call.cancel(cause)
	}
}

func (s *cacheState) finishRemember(key string, call *rememberCall, value interface{}, err error) {
	s.rememberMu.Lock()
	if !call.finished {
		call.finished = true
		call.value = value
		call.err = err
		if s.remembering[key] == call {
			delete(s.remembering, key)
		}
		close(call.done)
	}
	s.rememberMu.Unlock()
}
