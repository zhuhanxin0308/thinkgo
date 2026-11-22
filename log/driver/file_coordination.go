package driver

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/logcoord"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/winfile"
)

func withLogDirectoryLock(path string, expected os.FileInfo, operation func() error) (result error) {
	directory, err := openLogDirectory(path)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, directory.Close()) }()
	if _, err := verifyLogDirectory(path, directory, expected); err != nil {
		return err
	}
	guardPath := filepath.Join(path, logcoord.DirectoryGuard)
	if err := validateEmptyLogGuard(guardPath); err != nil {
		return err
	}
	return winfile.WithGuard(context.Background(), guardPath, func() error {
		// 等待目录锁期间目录可能已经被替换，旧目录上的锁不能授权操作新路径。
		if _, err := verifyLogDirectory(path, directory, expected); err != nil {
			return err
		}
		return operation()
	})
}

// retainLogOwner 只在空侧文件上登记；升级前已有的同名业务内容不能被锁定或回收。
func retainLogOwner(path string) (io.Closer, error) {
	if err := validateEmptyLogGuard(path); err != nil {
		return nil, err
	}
	return winfile.TrySharedGuard(path)
}

func validateEmptyLogGuard(path string) error {
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && (!info.Mode().IsRegular() || info.Size() != 0) {
		return errUnsafeLogFilePath
	}
	return nil
}

func verifyLogDirectory(path string, directory *os.File, expected os.FileInfo) (os.FileInfo, error) {
	opened, err := directory.Stat()
	if err != nil {
		return nil, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !current.IsDir() || !os.SameFile(opened, current) || expected != nil && !os.SameFile(expected, opened) {
		return nil, errUnsafeLogFilePath
	}
	return opened, nil
}

// retireIdleLogOwner 的调用方持有目录锁，所有共享持有者也只能在相同目录锁内登记。
// 因而独占探测成功后，到侧文件被移除前，不会有新的打开者或等待者绑定旧身份。
func retireIdleLogOwner(path string) (active bool, err error) {
	if err := validateEmptyLogGuard(path); err != nil {
		return false, err
	}
	err = winfile.TryWithGuard(context.Background(), path, func() error { return nil })
	if errors.Is(err, winfile.ErrGuardInUse) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, os.Remove(path)
}

func pruneIdleLogOwners(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		_, owner := logcoord.OwnedLog(entry.Name())
		if !owner {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != 0 {
			continue
		}
		// 独立检查侧文件，即使显式管理命令已删除数据，崩溃遗留的无人持有元数据也可回收。
		if _, err := retireIdleLogOwner(filepath.Join(directory, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func removeIdleLogFile(candidate managedLogFile) (bool, error) {
	owner := logcoord.OwnerPath(candidate.path)
	if _, canonical := logcoord.OwnedLog(filepath.Base(owner)); !canonical {
		// 旧规则还清理 _01 等历史段；驱动从不选它们写入，也不占用其近似侧文件名。
		return removeObservedLogFile(candidate)
	}
	if err := validateEmptyLogGuard(owner); err != nil {
		return false, err
	}
	// 总是按候选的确切路径取独占锁并持有至删除完成，不能以目录枚举中未看到侧文件作为授权。
	var removed bool
	err := winfile.TryWithGuard(context.Background(), owner, func() error {
		var err error
		removed, err = removeObservedLogFile(candidate)
		return err
	})
	if errors.Is(err, winfile.ErrGuardInUse) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return removed, os.Remove(owner)
}
