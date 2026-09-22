package migration

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

const (
	// MaximumRollbackBatches 限制单次回滚批次，避免公共 API 因异常输入产生巨额内存分配。
	MaximumRollbackBatches = 1000
)

// History 是数据库中已经成功应用的迁移记录。
type History struct {
	Name      string
	Checksum  string
	Batch     int64
	AppliedAt string
}

// Store 持久化迁移历史，并负责把迁移操作与历史写入放在同一数据库边界内。
type Store interface {
	Ensure(context.Context) error
	Inspect(context.Context) ([]History, bool, error)
	Applied(context.Context) ([]History, error)
	Apply(context.Context, Migration, int64) error
	Revert(context.Context, Migration) error
}

// LockedStore 将一次 Runner 操作绑定到数据库级互斥锁保护的存储实例。
// DatabaseStore 的 callback 存储还会固定到持锁的同一物理 SQL 会话。
type LockedStore interface {
	WithMigrationLock(context.Context, func(Store) error) error
}

// ReadLockedStore 在不初始化 schema、不推进 fencing token 的前提下提供一致只读迁移锁。
type ReadLockedStore interface {
	WithMigrationReadLock(context.Context, func(Store) error) error
}

// JournalOperation 标识 durable journal 中正在执行的迁移方向。
type JournalOperation string

const (
	JournalOperationApply  JournalOperation = "apply"
	JournalOperationRevert JournalOperation = "revert"
)

// JournalState 描述迁移开始、成功、失败和回滚后的持久状态。
type JournalState string

const (
	JournalApplying JournalState = "applying"
	JournalApplied  JournalState = "applied"
	JournalFailed   JournalState = "failed"
	JournalReverted JournalState = "reverted"
)

// JournalEntry 是迁移崩溃恢复和 fencing 校验所需的持久记录。
type JournalEntry struct {
	Name         string
	Checksum     string
	Batch        int64
	Operation    JournalOperation
	State        JournalState
	FencingToken int64
	StartedAt    string
	FinishedAt   string
	FailureCode  string
}

// RecoveryResolution 是操作员核验真实 schema 后选择的最终状态。
type RecoveryResolution string

const (
	RecoveryMarkApplied  RecoveryResolution = "mark_applied"
	RecoveryMarkReverted RecoveryResolution = "mark_reverted"
)

// JournalStore 暴露 durable journal 与显式恢复能力；普通测试 Store 可不实现。
type JournalStore interface {
	Journal(context.Context) ([]JournalEntry, error)
	ResolveDirty(context.Context, string, RecoveryResolution) error
}

// Result 描述一次迁移或回滚实际变更的名称和批次。
type Result struct {
	Batch int64
	Names []string
}

// Status 描述当前代码中的迁移及其数据库应用状态。
type Status struct {
	Name      string
	Checksum  string
	Applied   bool
	Batch     int64
	AppliedAt string
}

// Runner 串行执行一个应用的迁移，并在每次变更前校验完整历史。
type Runner struct {
	migrations []Migration
	store      Store
	mu         sync.Mutex
}

// NewRunner 冻结迁移注册表并创建运行器。
func NewRunner(registry *Registry, store Store) (*Runner, error) {
	if isNilMigration(store) {
		return nil, fmt.Errorf("%w: 迁移存储为空", ErrInvalidMigration)
	}
	migrations, err := registry.freezeSnapshot()
	if err != nil {
		return nil, err
	}
	return &Runner{migrations: migrations, store: store}, nil
}

// Apply 校验历史后按名称升序应用全部待执行迁移。
func (runner *Runner) Apply(ctx context.Context) (Result, error) {
	if runner == nil || ctx == nil {
		return Result{}, fmt.Errorf("%w: 迁移运行器或上下文为空", ErrInvalidMigration)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	var result Result
	err := runner.withMigrationLock(ctx, func(store Store) error {
		var applyErr error
		result, applyErr = runner.applyLocked(ctx, store)
		return applyErr
	})
	return result, err
}

func (runner *Runner) applyLocked(ctx context.Context, store Store) (Result, error) {
	history, _, maxBatch, err := runner.validatedHistory(ctx, store)
	if err != nil {
		return Result{}, err
	}
	appliedNames := make(map[string]struct{}, len(history))
	for _, applied := range history {
		appliedNames[applied.Name] = struct{}{}
	}
	batch := maxBatch + 1
	result := Result{Batch: batch}
	for _, current := range runner.migrations {
		if _, applied := appliedNames[current.Name()]; applied {
			continue
		}
		if err := store.Apply(ctx, current, batch); err != nil {
			return result, fmt.Errorf("应用迁移 %s 失败: %w", current.Name(), err)
		}
		result.Names = append(result.Names, current.Name())
	}
	if len(result.Names) == 0 {
		result.Batch = 0
	}
	return result, nil
}

// Rollback 按批次倒序回滚最近的若干批；每批内部按应用顺序逆序执行。
func (runner *Runner) Rollback(ctx context.Context, batches int) (Result, error) {
	if runner == nil || ctx == nil || batches <= 0 || batches > MaximumRollbackBatches {
		return Result{}, fmt.Errorf("%w: 回滚上下文必须有效，批次数必须在 1 到 %d 之间", ErrInvalidMigration, MaximumRollbackBatches)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	var result Result
	err := runner.withMigrationLock(ctx, func(store Store) error {
		var rollbackErr error
		result, rollbackErr = runner.rollbackLocked(ctx, store, batches)
		return rollbackErr
	})
	return result, err
}

func (runner *Runner) rollbackLocked(ctx context.Context, store Store, batches int) (Result, error) {
	history, definitions, _, err := runner.validatedHistory(ctx, store)
	if err != nil {
		return Result{}, err
	}
	sort.Slice(history, func(left, right int) bool {
		if history[left].Batch == history[right].Batch {
			return history[left].Name > history[right].Name
		}
		return history[left].Batch > history[right].Batch
	})
	selectedBatches := make(map[int64]struct{}, batches)
	result := Result{}
	for _, applied := range history {
		if _, selected := selectedBatches[applied.Batch]; !selected {
			if len(selectedBatches) >= batches {
				continue
			}
			selectedBatches[applied.Batch] = struct{}{}
			if result.Batch == 0 {
				result.Batch = applied.Batch
			}
		}
		current := definitions[applied.Name]
		if err := store.Revert(ctx, current); err != nil {
			return result, fmt.Errorf("回滚迁移 %s 失败: %w", applied.Name, err)
		}
		result.Names = append(result.Names, applied.Name)
	}
	return result, nil
}

// Status 校验完整历史后返回按迁移名称排序的状态快照。
func (runner *Runner) Status(ctx context.Context) ([]Status, error) {
	if runner == nil || ctx == nil {
		return nil, fmt.Errorf("%w: 迁移运行器或上下文为空", ErrInvalidMigration)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	var statuses []Status
	err := runner.withMigrationLock(ctx, func(store Store) error {
		history, _, _, historyErr := runner.validatedHistory(ctx, store)
		if historyErr != nil {
			return historyErr
		}
		statuses = runner.statusesFromHistory(history)
		return nil
	})
	return statuses, err
}

// InspectStatus 在不创建迁移历史表的前提下返回状态，供部署预检等只读场景使用。
func (runner *Runner) InspectStatus(ctx context.Context) ([]Status, bool, error) {
	if runner == nil || ctx == nil {
		return nil, false, fmt.Errorf("%w: 迁移运行器或上下文为空", ErrInvalidMigration)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	var statuses []Status
	initialized := false
	err := runner.withMigrationReadLock(ctx, func(store Store) error {
		history, storeInitialized, inspectErr := store.Inspect(ctx)
		initialized = storeInitialized
		if inspectErr != nil {
			return fmt.Errorf("检查迁移历史失败: %w", inspectErr)
		}
		if _, _, _, historyErr := runner.validateHistory(history); historyErr != nil {
			return historyErr
		}
		if initialized {
			if journalErr := runner.validateJournal(ctx, store, history); journalErr != nil {
				return journalErr
			}
		}
		statuses = runner.statusesFromHistory(history)
		return nil
	})
	return statuses, initialized, err
}

// ResolveDirty 在数据库级迁移锁内，把人工核验后的 schema 明确标记为已应用或已回滚。
func (runner *Runner) ResolveDirty(ctx context.Context, name string, resolution RecoveryResolution) error {
	if runner == nil || ctx == nil || !migrationNamePattern.MatchString(name) {
		return fmt.Errorf("%w: 恢复参数非法", ErrInvalidMigration)
	}
	if resolution != RecoveryMarkApplied && resolution != RecoveryMarkReverted {
		return fmt.Errorf("%w: 未知恢复结论 %q", ErrInvalidMigration, resolution)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.withMigrationLock(ctx, func(store Store) error {
		if err := store.Ensure(ctx); err != nil {
			return fmt.Errorf("初始化迁移历史失败: %w", err)
		}
		journalStore, ok := store.(JournalStore)
		if !ok || journalStore == nil {
			return fmt.Errorf("%w: 当前迁移存储不支持 durable journal", ErrInvalidMigration)
		}
		history, err := store.Applied(ctx)
		if err != nil {
			return fmt.Errorf("读取迁移历史失败: %w", err)
		}
		_, definitions, _, err := runner.validateHistory(history)
		if err != nil {
			return err
		}
		entries, err := journalStore.Journal(ctx)
		if err != nil {
			return fmt.Errorf("读取迁移 journal 失败: %w", err)
		}
		entry, err := runner.validateRecoveryTarget(entries, history, definitions, name)
		if err != nil {
			return err
		}
		if err := journalStore.ResolveDirty(ctx, entry.Name, resolution); err != nil {
			return fmt.Errorf("恢复 dirty 迁移 %s 失败: %w", name, err)
		}
		_, _, _, err = runner.validatedHistory(ctx, store)
		return err
	})
}

func (runner *Runner) validateRecoveryTarget(
	entries []JournalEntry,
	history []History,
	definitions map[string]Migration,
	name string,
) (JournalEntry, error) {
	historyByName := make(map[string]History, len(history))
	for _, applied := range history {
		historyByName[applied.Name] = applied
	}
	seen := make(map[string]struct{}, len(entries))
	var target JournalEntry
	found := false
	for _, entry := range entries {
		if err := validateJournalEntryFields(entry); err != nil {
			return JournalEntry{}, err
		}
		if _, duplicate := seen[entry.Name]; duplicate {
			return JournalEntry{}, fmt.Errorf("%w: journal 记录 %q 重复", ErrMigrationDrift, entry.Name)
		}
		seen[entry.Name] = struct{}{}
		applied, hasHistory := historyByName[entry.Name]
		switch entry.State {
		case JournalApplied:
			if !hasHistory || applied.Checksum != entry.Checksum || applied.Batch != entry.Batch {
				return JournalEntry{}, fmt.Errorf("%w: journal 与迁移历史不一致 %s", ErrMigrationDrift, entry.Name)
			}
		case JournalReverted:
			if hasHistory {
				return JournalEntry{}, fmt.Errorf("%w: 已回滚 journal 仍存在历史 %s", ErrMigrationDrift, entry.Name)
			}
		case JournalApplying, JournalFailed:
			if hasHistory && (applied.Checksum != entry.Checksum || applied.Batch != entry.Batch) {
				return JournalEntry{}, fmt.Errorf("%w: dirty journal 与迁移历史不一致 %s", ErrMigrationDrift, entry.Name)
			}
		}
		if entry.Name == name {
			target = entry
			found = entry.State == JournalApplying || entry.State == JournalFailed
		}
	}
	if !found {
		return JournalEntry{}, fmt.Errorf("%w: %s", ErrMigrationNotDirty, name)
	}
	current, exists := definitions[target.Name]
	if !exists {
		return JournalEntry{}, fmt.Errorf("%w: %s", ErrMigrationMissing, target.Name)
	}
	if current.Checksum() != target.Checksum {
		return JournalEntry{}, fmt.Errorf("%w: %s", ErrMigrationDrift, target.Name)
	}
	return target, nil
}

func (runner *Runner) statusesFromHistory(history []History) []Status {
	applied := make(map[string]History, len(history))
	for _, entry := range history {
		applied[entry.Name] = entry
	}
	statuses := make([]Status, 0, len(runner.migrations))
	for _, current := range runner.migrations {
		entry, exists := applied[current.Name()]
		statuses = append(statuses, Status{
			Name:      current.Name(),
			Checksum:  current.Checksum(),
			Applied:   exists,
			Batch:     entry.Batch,
			AppliedAt: entry.AppliedAt,
		})
	}
	return statuses
}

func (runner *Runner) validatedHistory(ctx context.Context, store Store) ([]History, map[string]Migration, int64, error) {
	if err := store.Ensure(ctx); err != nil {
		return nil, nil, 0, fmt.Errorf("初始化迁移历史失败: %w", err)
	}
	history, err := store.Applied(ctx)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("读取迁移历史失败: %w", err)
	}
	validated, definitions, maxBatch, err := runner.validateHistory(history)
	if err != nil {
		return nil, nil, 0, err
	}
	if err := runner.validateJournal(ctx, store, history); err != nil {
		return nil, nil, 0, err
	}
	return validated, definitions, maxBatch, nil
}

func (runner *Runner) withMigrationLock(ctx context.Context, callback func(Store) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if locked, ok := runner.store.(LockedStore); ok && locked != nil {
		return locked.WithMigrationLock(ctx, callback)
	}
	return callback(runner.store)
}

func (runner *Runner) withMigrationReadLock(ctx context.Context, callback func(Store) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if locked, ok := runner.store.(ReadLockedStore); ok && locked != nil {
		return locked.WithMigrationReadLock(ctx, callback)
	}
	return callback(runner.store)
}

func (runner *Runner) validateJournal(ctx context.Context, store Store, history []History) error {
	journalStore, ok := store.(JournalStore)
	if !ok || journalStore == nil {
		return nil
	}
	entries, err := journalStore.Journal(ctx)
	if err != nil {
		return fmt.Errorf("读取迁移 journal 失败: %w", err)
	}
	historyByName := make(map[string]History, len(history))
	for _, applied := range history {
		historyByName[applied.Name] = applied
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := validateJournalEntryFields(entry); err != nil {
			return err
		}
		if _, duplicate := seen[entry.Name]; duplicate {
			return fmt.Errorf("%w: journal 记录 %q 重复", ErrMigrationDrift, entry.Name)
		}
		seen[entry.Name] = struct{}{}
		switch entry.State {
		case JournalApplying, JournalFailed:
			return fmt.Errorf("%w: %s state=%s operation=%s token=%d", ErrMigrationDirty, entry.Name, entry.State, entry.Operation, entry.FencingToken)
		case JournalApplied:
			applied, exists := historyByName[entry.Name]
			if !exists || applied.Checksum != entry.Checksum || applied.Batch != entry.Batch {
				return fmt.Errorf("%w: journal 与迁移历史不一致 %s", ErrMigrationDrift, entry.Name)
			}
		case JournalReverted:
			if _, exists := historyByName[entry.Name]; exists {
				return fmt.Errorf("%w: 已回滚 journal 仍存在历史 %s", ErrMigrationDrift, entry.Name)
			}
		default:
			return fmt.Errorf("%w: journal 记录 %q 的状态非法", ErrMigrationDrift, entry.Name)
		}
	}
	return nil
}

func validateJournalEntryFields(entry JournalEntry) error {
	if !migrationNamePattern.MatchString(entry.Name) || !migrationChecksumPattern.MatchString(entry.Checksum) || entry.Batch <= 0 || entry.FencingToken <= 0 {
		return fmt.Errorf("%w: journal 记录 %q 的字段非法", ErrMigrationDrift, entry.Name)
	}
	if entry.Operation != JournalOperationApply && entry.Operation != JournalOperationRevert {
		return fmt.Errorf("%w: journal 记录 %q 的操作非法", ErrMigrationDrift, entry.Name)
	}
	if entry.State == JournalApplied && entry.Operation != JournalOperationApply ||
		entry.State == JournalReverted && entry.Operation != JournalOperationRevert {
		return fmt.Errorf("%w: journal 记录 %q 的操作与最终状态不一致", ErrMigrationDrift, entry.Name)
	}
	switch entry.State {
	case JournalApplying, JournalApplied, JournalFailed, JournalReverted:
		return nil
	default:
		return fmt.Errorf("%w: journal 记录 %q 的状态非法", ErrMigrationDrift, entry.Name)
	}
}

func (runner *Runner) validateHistory(history []History) ([]History, map[string]Migration, int64, error) {
	definitions := make(map[string]Migration, len(runner.migrations))
	for _, current := range runner.migrations {
		definitions[current.Name()] = current
	}
	seen := make(map[string]struct{}, len(history))
	maxBatch := int64(0)
	for _, applied := range history {
		if _, duplicate := seen[applied.Name]; duplicate || applied.Batch <= 0 {
			return nil, nil, 0, fmt.Errorf("%w: 历史记录 %q 重复或批次非法", ErrMigrationDrift, applied.Name)
		}
		seen[applied.Name] = struct{}{}
		current, exists := definitions[applied.Name]
		if !exists {
			return nil, nil, 0, fmt.Errorf("%w: %s", ErrMigrationMissing, applied.Name)
		}
		if current.Checksum() != applied.Checksum {
			return nil, nil, 0, fmt.Errorf("%w: %s", ErrMigrationDrift, applied.Name)
		}
		if applied.Batch > maxBatch {
			maxBatch = applied.Batch
		}
	}
	return history, definitions, maxBatch, nil
}
