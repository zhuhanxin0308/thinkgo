package migration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type runnerWaitStore struct {
	migrationStoreProbe
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (store *runnerWaitStore) WithMigrationLock(_ context.Context, callback func(Store) error) error {
	if store.calls.Add(1) == 1 {
		close(store.entered)
		<-store.release
	}
	return callback(store)
}

func (store *runnerWaitStore) WithMigrationReadLock(ctx context.Context, callback func(Store) error) error {
	return store.WithMigrationLock(ctx, callback)
}

// observedRunnerWait 在等待者登记取消通道时发出信号，以覆盖真正排队后的取消。
type observedRunnerWait struct {
	context.Context
	queued chan struct{}
	once   sync.Once
}

func (ctx *observedRunnerWait) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.queued) })
	return ctx.Context.Done()
}

func TestRunnerCancelsQueuedOperationsBeforeStoreLock(t *testing.T) {
	store := &runnerWaitStore{entered: make(chan struct{}), release: make(chan struct{})}
	runner, err := NewRunner(NewRegistry(), store)
	if err != nil {
		t.Fatal(err)
	}
	holderDone := make(chan error, 1)
	go func() {
		_, applyErr := runner.Apply(context.Background())
		holderDone <- applyErr
	}()
	<-store.entered
	t.Cleanup(func() {
		close(store.release)
		if err := <-holderDone; err != nil {
			t.Errorf("先前持锁者应正常完成: %v", err)
		}
		if _, err := runner.Status(context.Background()); err != nil {
			t.Errorf("取消排队操作后无法再次取得运行器: %v", err)
		}
	})

	for name, run := range map[string]func(context.Context) error{
		"Apply": func(ctx context.Context) error {
			_, err := runner.Apply(ctx)
			return err
		},
		"Rollback": func(ctx context.Context) error {
			_, err := runner.Rollback(ctx, 1)
			return err
		},
		"Status": func(ctx context.Context) error {
			_, err := runner.Status(ctx)
			return err
		},
		"InspectStatus": func(ctx context.Context) error {
			_, _, err := runner.InspectStatus(ctx)
			return err
		},
		"ResolveDirty": func(ctx context.Context) error {
			return runner.ResolveDirty(ctx, "202608150001_create_users", RecoveryMarkApplied)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wait := &observedRunnerWait{Context: ctx, queued: make(chan struct{})}
			result := make(chan error, 1)
			go func() { result <- run(wait) }()
			const watchdog = 2 * time.Second
			timer := time.NewTimer(watchdog)
			defer timer.Stop()
			select {
			case <-wait.queued:
			case err := <-result:
				t.Fatalf("持锁操作尚未完成，排队操作却先退出: %v", err)
			case <-timer.C:
				t.Fatal("等待运行器时未监听调用方取消")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("取消等待应返回 context.Canceled: %v", err)
				}
			case <-timer.C:
				t.Fatal("调用方取消后仍在等待运行器")
			}
		})
	}
	t.Run("截止时间", func(t *testing.T) {
		const deadline = 20 * time.Millisecond
		ctx, cancel := context.WithTimeout(t.Context(), deadline)
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, _, err := runner.InspectStatus(ctx)
			result <- err
		}()
		select {
		case err := <-result:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("排队超时应保留截止错误: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("持锁者被阻塞时，排队调用未按截止时间返回")
		}
	})
	if calls := store.calls.Load(); calls != 1 {
		t.Errorf("排队取消的操作进入了存储锁: 调用次数 %d", calls)
	}
}

func TestRunnerReleasesLocalLockWhenStorePanics(t *testing.T) {
	const panicValue = "迁移回调异常"
	current, err := New("202608150001_create_users", firstChecksum, func(context.Context, Executor) error {
		panic(panicValue)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := registry.Register(current); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(registry, &migrationStoreProbe{})
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recovered := recover(); recovered != panicValue {
				t.Errorf("迁移回调 panic 原因发生变化: %v", recovered)
			}
		}()
		_, _ = runner.Apply(t.Context())
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	statuses, _, err := runner.InspectStatus(ctx)
	if err != nil || len(statuses) != 1 || statuses[0].Applied {
		t.Fatalf("异常退出后未能读取原先的迁移状态: %v, %v", statuses, err)
	}
}
