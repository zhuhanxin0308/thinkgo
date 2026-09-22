package migration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type coordinatedMigrationState struct {
	mu      sync.Mutex
	history []History
	journal []JournalEntry
	upCalls atomic.Int64
}

type coordinatedMigrationStore struct {
	state *coordinatedMigrationState
}

func (store *coordinatedMigrationStore) WithMigrationLock(ctx context.Context, callback func(Store) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.state.mu.Lock()
	defer store.state.mu.Unlock()
	return callback(store)
}

func (store *coordinatedMigrationStore) WithMigrationReadLock(ctx context.Context, callback func(Store) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.state.mu.Lock()
	defer store.state.mu.Unlock()
	return callback(store)
}

func (*coordinatedMigrationStore) Ensure(context.Context) error { return nil }

func (store *coordinatedMigrationStore) Applied(context.Context) ([]History, error) {
	return append([]History(nil), store.state.history...), nil
}

func (store *coordinatedMigrationStore) Inspect(context.Context) ([]History, bool, error) {
	return append([]History(nil), store.state.history...), true, nil
}

func (store *coordinatedMigrationStore) Apply(ctx context.Context, current Migration, batch int64) error {
	if err := current.Up(ctx, nil); err != nil {
		return err
	}
	store.state.history = append(store.state.history, History{Name: current.Name(), Checksum: current.Checksum(), Batch: batch})
	return nil
}

func (store *coordinatedMigrationStore) Revert(ctx context.Context, current Migration) error {
	if err := current.Down(ctx, nil); err != nil {
		return err
	}
	store.state.history = nil
	return nil
}

func (store *coordinatedMigrationStore) Journal(context.Context) ([]JournalEntry, error) {
	return append([]JournalEntry(nil), store.state.journal...), nil
}

func (store *coordinatedMigrationStore) ResolveDirty(_ context.Context, name string, resolution RecoveryResolution) error {
	for index := range store.state.journal {
		entry := &store.state.journal[index]
		if entry.Name != name {
			continue
		}
		switch resolution {
		case RecoveryMarkApplied:
			entry.Operation = JournalOperationApply
			entry.State = JournalApplied
			store.state.history = []History{{Name: entry.Name, Checksum: entry.Checksum, Batch: entry.Batch}}
		case RecoveryMarkReverted:
			entry.Operation = JournalOperationRevert
			entry.State = JournalReverted
			store.state.history = nil
		default:
			return ErrInvalidMigration
		}
		return nil
	}
	return ErrMigrationMissing
}

func newCoordinatedRunner(t *testing.T, state *coordinatedMigrationState, current Migration) *Runner {
	t.Helper()
	registry := NewRegistry()
	if err := registry.Register(current); err != nil {
		t.Fatalf("注册并发迁移失败: %v", err)
	}
	runner, err := NewRunner(registry, &coordinatedMigrationStore{state: state})
	if err != nil {
		t.Fatalf("创建并发迁移运行器失败: %v", err)
	}
	return runner
}

// TestRunnerCoordinatesIndependentRunnersWithStoreLock 验证不同 Runner 依靠存储级锁只执行一次迁移。
func TestRunnerCoordinatesIndependentRunnersWithStoreLock(t *testing.T) {
	state := &coordinatedMigrationState{}
	current, err := New(
		"202608150001_create_users",
		firstChecksum,
		func(context.Context, Executor) error {
			state.upCalls.Add(1)
			time.Sleep(20 * time.Millisecond)
			return nil
		},
		func(context.Context, Executor) error { return nil },
	)
	if err != nil {
		t.Fatalf("创建并发迁移失败: %v", err)
	}
	runners := []*Runner{newCoordinatedRunner(t, state, current), newCoordinatedRunner(t, state, current)}
	start := make(chan struct{})
	results := make(chan Result, len(runners))
	errorsChannel := make(chan error, len(runners))
	var wait sync.WaitGroup
	for _, runner := range runners {
		wait.Add(1)
		go func(currentRunner *Runner) {
			defer wait.Done()
			<-start
			result, applyErr := currentRunner.Apply(context.Background())
			results <- result
			errorsChannel <- applyErr
		}(runner)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	for applyErr := range errorsChannel {
		if applyErr != nil {
			t.Fatalf("并发 Runner 执行失败: %v", applyErr)
		}
	}
	appliedResults := 0
	for result := range results {
		if len(result.Names) == 1 {
			appliedResults++
		}
	}
	if appliedResults != 1 || state.upCalls.Load() != 1 {
		t.Fatalf("迁移没有被全局串行化: applied_results=%d up_calls=%d", appliedResults, state.upCalls.Load())
	}
}

// TestRunnerInspectWaitsForIndependentMigrationSnapshot 验证只读预检不会跨越另一 Runner 的提交边界读取撕裂状态。
func TestRunnerInspectWaitsForIndependentMigrationSnapshot(t *testing.T) {
	state := &coordinatedMigrationState{}
	entered := make(chan struct{})
	release := make(chan struct{})
	current, err := New(
		"202608150001_create_users",
		firstChecksum,
		func(context.Context, Executor) error {
			close(entered)
			<-release
			return nil
		},
		func(context.Context, Executor) error { return nil },
	)
	if err != nil {
		t.Fatalf("创建只读快照迁移失败: %v", err)
	}
	applyRunner := newCoordinatedRunner(t, state, current)
	inspectRunner := newCoordinatedRunner(t, state, current)
	applyDone := make(chan error, 1)
	go func() {
		_, applyErr := applyRunner.Apply(context.Background())
		applyDone <- applyErr
	}()
	<-entered
	type inspectResult struct {
		statuses    []Status
		initialized bool
		err         error
	}
	inspectDone := make(chan inspectResult, 1)
	go func() {
		statuses, initialized, inspectErr := inspectRunner.InspectStatus(context.Background())
		inspectDone <- inspectResult{statuses: statuses, initialized: initialized, err: inspectErr}
	}()
	select {
	case result := <-inspectDone:
		close(release)
		t.Fatalf("只读预检没有等待迁移提交: %#v", result)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if applyErr := <-applyDone; applyErr != nil {
		t.Fatalf("并发迁移失败: %v", applyErr)
	}
	result := <-inspectDone
	if result.err != nil || !result.initialized || len(result.statuses) != 1 || !result.statuses[0].Applied {
		t.Fatalf("只读预检没有返回提交后一致快照: %#v", result)
	}
}

// TestRunnerRejectsDirtyJournalBeforeDDL 验证 applying/failed 状态在任何新 DDL 前强制失败。
func TestRunnerRejectsDirtyJournalBeforeDDL(t *testing.T) {
	for _, journalState := range []JournalState{JournalApplying, JournalFailed} {
		t.Run(string(journalState), func(t *testing.T) {
			state := &coordinatedMigrationState{journal: []JournalEntry{{
				Name:         "202608150001_create_users",
				Checksum:     firstChecksum,
				Batch:        1,
				Operation:    JournalOperationApply,
				State:        journalState,
				FencingToken: 1,
			}}}
			current := mustMigration(t, "202608150001_create_users", firstChecksum)
			runner := newCoordinatedRunner(t, state, current)
			if _, err := runner.Apply(context.Background()); !errors.Is(err, ErrMigrationDirty) {
				t.Fatalf("dirty journal 没有阻止迁移: %v", err)
			}
			if state.upCalls.Load() != 0 || len(state.history) != 0 {
				t.Fatalf("dirty 状态后仍执行了变更: up_calls=%d history=%#v", state.upCalls.Load(), state.history)
			}
		})
	}
}

// TestRunnerResolvesDirtyStateExplicitly 验证只有显式确认数据库最终状态后才允许继续迁移。
func TestRunnerResolvesDirtyStateExplicitly(t *testing.T) {
	state := &coordinatedMigrationState{journal: []JournalEntry{{
		Name:         "202608150001_create_users",
		Checksum:     firstChecksum,
		Batch:        1,
		Operation:    JournalOperationApply,
		State:        JournalFailed,
		FencingToken: 1,
	}}}
	current := mustMigration(t, "202608150001_create_users", firstChecksum)
	runner := newCoordinatedRunner(t, state, current)
	if err := runner.ResolveDirty(context.Background(), current.Name(), RecoveryMarkApplied); err != nil {
		t.Fatalf("显式确认迁移已应用失败: %v", err)
	}
	status, err := runner.Status(context.Background())
	if err != nil || len(status) != 1 || !status[0].Applied {
		t.Fatalf("恢复后的迁移状态错误: status=%#v err=%v", status, err)
	}
	if err = runner.ResolveDirty(context.Background(), current.Name(), RecoveryMarkReverted); !errors.Is(err, ErrMigrationNotDirty) {
		t.Fatalf("非 dirty 迁移不应被再次恢复: %v", err)
	}
}

// TestRunnerRejectsDirtyChecksumBeforeRecoveryMutation 验证代码漂移会在写入恢复最终态之前失败。
func TestRunnerRejectsDirtyChecksumBeforeRecoveryMutation(t *testing.T) {
	for _, resolution := range []RecoveryResolution{RecoveryMarkApplied, RecoveryMarkReverted} {
		t.Run(string(resolution), func(t *testing.T) {
			name := "202608150001_create_users"
			state := &coordinatedMigrationState{journal: []JournalEntry{{
				Name: name, Checksum: firstChecksum, Batch: 1,
				Operation: JournalOperationApply, State: JournalFailed, FencingToken: 1,
			}}}
			runner := newCoordinatedRunner(t, state, mustMigration(t, name, secondChecksum))
			if err := runner.ResolveDirty(context.Background(), name, resolution); !errors.Is(err, ErrMigrationDrift) {
				t.Fatalf("checksum 漂移没有在恢复写入前被拒绝: %v", err)
			}
			if len(state.history) != 0 || len(state.journal) != 1 || state.journal[0].State != JournalFailed {
				t.Fatalf("失败预检后恢复状态被永久改写: history=%#v journal=%#v", state.history, state.journal)
			}
		})
	}
}

// TestRunnerLockHonorsCanceledContext 验证锁等待入口不会吞掉上下文取消。
func TestRunnerLockHonorsCanceledContext(t *testing.T) {
	state := &coordinatedMigrationState{}
	runner := newCoordinatedRunner(t, state, mustMigration(t, "202608150001_create_users", firstChecksum))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.Apply(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("锁入口没有传播上下文取消: %v", err)
	}
}

// TestRunnerRejectsCorruptJournalCombinations 验证 journal 最终态、历史和字段必须形成一致证明。
func TestRunnerRejectsCorruptJournalCombinations(t *testing.T) {
	name := "202608150001_create_users"
	validHistory := History{Name: name, Checksum: firstChecksum, Batch: 1}
	validEntry := JournalEntry{
		Name: name, Checksum: firstChecksum, Batch: 1,
		Operation: JournalOperationApply, State: JournalApplied, FencingToken: 1,
	}
	for _, testCase := range []struct {
		name    string
		history []History
		journal []JournalEntry
	}{
		{name: "applied_without_history", journal: []JournalEntry{validEntry}},
		{name: "reverted_with_history", history: []History{validHistory}, journal: []JournalEntry{func() JournalEntry {
			entry := validEntry
			entry.State = JournalReverted
			entry.Operation = JournalOperationRevert
			return entry
		}()}},
		{name: "invalid_operation", history: []History{validHistory}, journal: []JournalEntry{func() JournalEntry {
			entry := validEntry
			entry.Operation = JournalOperation("sideways")
			return entry
		}()}},
		{name: "invalid_state", journal: []JournalEntry{func() JournalEntry {
			entry := validEntry
			entry.State = JournalState("unknown")
			return entry
		}()}},
		{name: "invalid_token", journal: []JournalEntry{func() JournalEntry {
			entry := validEntry
			entry.FencingToken = 0
			return entry
		}()}},
		{name: "duplicate", history: []History{validHistory}, journal: []JournalEntry{validEntry, validEntry}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			state := &coordinatedMigrationState{history: testCase.history, journal: testCase.journal}
			runner := newCoordinatedRunner(t, state, mustMigration(t, name, firstChecksum))
			if _, err := runner.Status(context.Background()); !errors.Is(err, ErrMigrationDrift) {
				t.Fatalf("损坏 journal 没有被拒绝: %v", err)
			}
		})
	}
}
