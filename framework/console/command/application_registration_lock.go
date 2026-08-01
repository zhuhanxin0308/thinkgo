package command

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const applicationRegistrationLockFilename = ".application.go.lock"

type applicationRegistrationLock struct {
	root *os.Root
	file *os.File
}

// acquireApplicationRegistrationLock 使用操作系统文件锁串行化跨进程的 AST 读改写。
func acquireApplicationRegistrationLock(basePath, relativeDirectory string) (*applicationRegistrationLock, error) {
	cleanDirectory := filepath.Clean(relativeDirectory)
	if cleanDirectory == "." || filepath.IsAbs(relativeDirectory) || cleanDirectory == ".." || strings.HasPrefix(cleanDirectory, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("应用注册锁目录非法: %q", relativeDirectory)
	}
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return nil, fmt.Errorf("打开应用注册锁根目录失败: %w", err)
	}
	if err := root.MkdirAll(relativeDirectory, 0o755); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("创建应用注册锁目录失败: %w", err)
	}
	lockPath := filepath.Join(relativeDirectory, applicationRegistrationLockFilename)
	file, err := root.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("打开应用注册锁文件失败: %w", err)
	}
	if err := lockApplicationRegistrationFile(file); err != nil {
		_ = file.Close()
		_ = root.Close()
		return nil, fmt.Errorf("获取应用注册锁失败: %w", err)
	}
	return &applicationRegistrationLock{root: root, file: file}, nil
}

func (lock *applicationRegistrationLock) Close() error {
	if lock == nil {
		return nil
	}
	return errors.Join(
		unlockApplicationRegistrationFile(lock.file),
		closeApplicationRegistrationFile(lock.file),
		lock.root.Close(),
	)
}
