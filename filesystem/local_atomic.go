package filesystem

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
)

const (
	localTemporaryPrefix   = ".thinkgo-tmp-"
	localTemporaryAttempts = 16
)

// ErrFilesystemOperationCommitted 表示操作已修改可见状态，但后续持久化或属性步骤失败。
// 调用方收到该错误后不能假定目标仍处于旧状态，应重新读取目标确认结果。
var ErrFilesystemOperationCommitted = errors.New("文件系统操作已提交")

func (disk *Local) writeReaderWithSource(
	filePath string,
	contents io.Reader,
	source io.Closer,
	sourceCloseMessage string,
	optionSets []map[string]interface{},
) (resultErr error) {
	sourceHandled := false
	if source != nil {
		defer func() {
			if sourceHandled {
				return
			}
			resultErr = errors.Join(resultErr, wrapOperationError(sourceCloseMessage, disk.closeOwnedSource(source)))
		}()
	}

	logicalPath, err := normalizeFilePath(filePath)
	if err != nil {
		return err
	}
	options, err := singleOptions(optionSets)
	if err != nil {
		return err
	}
	visibility, visibilityExplicit, err := disk.writeVisibility(options)
	if err != nil {
		return err
	}
	directoryVisibility, _, err := optionVisibility(options, "directory_visibility", disk.configuration.Visibility, false)
	if err != nil {
		return err
	}

	disk.mu.RLock()
	defer disk.mu.RUnlock()
	if err = disk.ensureOpenLocked(); err != nil {
		return err
	}
	existed := false
	fileMode := disk.configuration.fileMode(visibility)
	if information, statErr := disk.root.Stat(logicalPath); statErr == nil {
		if information.IsDir() {
			return fmt.Errorf("写入目标是目录: %s", logicalPath)
		}
		existed = true
		if !visibilityExplicit {
			// 原实现截断既有文件时不会改变权限；临时文件发布也必须保留这一兼容语义。
			fileMode = information.Mode().Perm()
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("检查写入目标失败: %w", statErr)
	}
	if err = disk.ensureParentDirectoryLocked(logicalPath, directoryVisibility); err != nil {
		return err
	}
	committed, err := disk.replaceReaderLocked(
		logicalPath,
		contents,
		source,
		fileMode,
		"写入文件内容失败",
		sourceCloseMessage,
	)
	sourceHandled = source != nil
	if committed && (!existed || visibilityExplicit) {
		disk.rememberVisibility(logicalPath, visibility)
	}
	return err
}

func (disk *Local) replaceReaderLocked(
	target string,
	contents io.Reader,
	source io.Closer,
	mode os.FileMode,
	copyMessage string,
	sourceCloseMessage string,
) (bool, error) {
	temporaryName, prepareErr := disk.prepareAtomicFileLocked(target, contents, mode, copyMessage)
	sourceCloseErr := error(nil)
	if source != nil {
		sourceCloseErr = wrapOperationError(sourceCloseMessage, disk.closeOwnedSource(source))
	}
	if prepareErr != nil || sourceCloseErr != nil {
		cleanupErr := error(nil)
		if temporaryName != "" {
			cleanupErr = disk.removeTemporaryLocked(temporaryName)
		}
		return false, errors.Join(prepareErr, sourceCloseErr, cleanupErr)
	}
	return disk.publishAtomicFileLocked(temporaryName, target)
}

func (disk *Local) prepareAtomicFileLocked(
	target string,
	contents io.Reader,
	mode os.FileMode,
	copyMessage string,
) (string, error) {
	temporaryName, temporary, err := disk.createAtomicTemporaryLocked(target)
	if err != nil {
		return "", err
	}
	fail := func(operationErr error, closeAttempted bool) (string, error) {
		closeErr := error(nil)
		if closeAttempted {
			// 关闭实现可能在返回错误前后关闭句柄，直接再关闭一次仅用于确保可清理。
			_ = temporary.Close()
		} else {
			closeErr = disk.operations.closeTemporary(temporary)
			if closeErr != nil {
				_ = temporary.Close()
			}
		}
		return temporaryName, errors.Join(
			operationErr,
			wrapOperationError("关闭临时文件失败", closeErr),
			disk.removeTemporaryLocked(temporaryName),
		)
	}
	if _, err = disk.operations.copy(temporary, contents); err != nil {
		return fail(wrapOperationError(copyMessage, err), false)
	}
	if err = disk.operations.chmod(disk.root, temporaryName, mode); err != nil {
		return fail(fmt.Errorf("设置临时文件权限失败: %w", err), false)
	}
	if err = disk.operations.syncFile(temporary); err != nil {
		return fail(fmt.Errorf("同步临时文件失败: %w", err), false)
	}
	if err = disk.operations.closeTemporary(temporary); err != nil {
		return fail(fmt.Errorf("关闭临时文件失败: %w", err), true)
	}
	return temporaryName, nil
}

func (disk *Local) createAtomicTemporaryLocked(target string) (string, *os.File, error) {
	parent := path.Dir(target)
	if parent == "." {
		parent = ""
	}
	for attempt := 0; attempt < localTemporaryAttempts; attempt++ {
		randomBytes := make([]byte, 16)
		if _, err := rand.Read(randomBytes); err != nil {
			return "", nil, fmt.Errorf("生成临时文件名失败: %w", err)
		}
		temporaryName := localTemporaryPrefix + hex.EncodeToString(randomBytes)
		if parent != "" {
			temporaryName = path.Join(parent, temporaryName)
		}
		temporary, err := disk.root.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return temporaryName, temporary, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, fmt.Errorf("创建同目录临时文件失败: %w", err)
		}
	}
	return "", nil, fmt.Errorf("创建同目录临时文件失败: 随机名称连续冲突 %d 次", localTemporaryAttempts)
}

func (disk *Local) publishAtomicFileLocked(temporaryName string, target string) (bool, error) {
	if err := disk.operations.replace(disk.root, temporaryName, target); err != nil {
		return false, errors.Join(
			fmt.Errorf("原子替换目标失败: %w", err),
			disk.removeTemporaryLocked(temporaryName),
		)
	}
	if err := disk.operations.syncDirectory(disk.root, path.Dir(target)); err != nil {
		return true, errors.Join(
			ErrFilesystemOperationCommitted,
			fmt.Errorf("同步目标目录失败: %w", err),
		)
	}
	return true, nil
}

func (disk *Local) closeOwnedSource(source io.Closer) error {
	if source == nil {
		return nil
	}
	err := disk.operations.closeSource(source)
	if err != nil {
		// 某些实现会在关闭句柄后才报告刷盘错误；再次关闭只负责释放仍存活的句柄。
		fallbackErr := source.Close()
		if fallbackErr != nil && !errors.Is(fallbackErr, os.ErrInvalid) {
			return errors.Join(err, fmt.Errorf("再次关闭源文件失败: %w", fallbackErr))
		}
	}
	return err
}

func (disk *Local) removeTemporaryLocked(temporaryName string) error {
	if temporaryName == "" {
		return nil
	}
	err := disk.root.Remove(temporaryName)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("清理临时文件失败: %w", err)
}
