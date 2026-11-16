package winfile

import (
	"context"
	"errors"
	"os"
)

var (
	// ErrUnsafeGuard 表示锁路径无法安全绑定到稳定文件。
	ErrUnsafeGuard = errors.New("锁路径不是稳定的普通文件")
	// ErrGuardInUse 表示非阻塞获取锁时另一个持有者仍在运行。
	ErrGuardInUse = errors.New("文件锁正在使用")
)

// WithGuard 在持久文件上持有操作系统范围锁；不删除文件，不以时间过期转移所有权。
// 进程退出时内核释放锁，暂停的写者恢复前其他进程不能进入临界区。
func WithGuard(ctx context.Context, path string, operation func() error) error {
	return withGuard(ctx, path, lockGuard, operation)
}

// TryWithGuard 在持有者存在时立即返回 ErrGuardInUse，其他约束与 WithGuard 一致。
func TryWithGuard(ctx context.Context, path string, operation func() error) error {
	return withGuard(ctx, path, func(_ context.Context, handle *os.File) error {
		locked, err := tryGuardLock(handle)
		if err == nil && !locked {
			return ErrGuardInUse
		}
		return err
	}, operation)
}

func withGuard(ctx context.Context, path string, lock func(context.Context, *os.File) error, operation func() error) (result error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if operation == nil {
		return os.ErrInvalid
	}
	before, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && (before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular()) {
		return ErrUnsafeGuard
	}
	handle, err := openGuard(path)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, handle.Close()) }()
	if err := lock(ctx, handle); err != nil {
		return err
	}
	defer func() { result = errors.Join(result, unlockGuard(handle)) }()
	opened, openedErr := handle.Stat()
	current, currentErr := os.Lstat(path)
	if openedErr != nil || currentErr != nil {
		return errors.Join(ErrUnsafeGuard, openedErr, currentErr)
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() ||
		!opened.Mode().IsRegular() || !os.SameFile(opened, current) ||
		(before != nil && !os.SameFile(before, opened)) {
		return ErrUnsafeGuard
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
}
