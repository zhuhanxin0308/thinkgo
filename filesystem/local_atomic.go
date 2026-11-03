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

// ErrUnsafeLocalWriteDirectory 表示原子写入依赖的目录不能安全用于暂存与发布。
// Unix 检查 POSIX 所有者和权限位；扩展 ACL 的信任条件见包文档。
var ErrUnsafeLocalWriteDirectory = errors.New("本地原子写入目录不可信")

var errLocalTemporaryChanged = errors.New("本地原子写入临时项已被替换")

type localAtomicTemporary struct {
	name        string
	file        *os.File
	information os.FileInfo
}

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
	if err = disk.ensureAtomicParentDirectoryLocked(logicalPath, directoryVisibility); err != nil {
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
	temporary, prepareErr := disk.prepareAtomicFileLocked(target, contents, mode, copyMessage)
	sourceCloseErr := error(nil)
	if source != nil {
		sourceCloseErr = wrapOperationError(sourceCloseMessage, disk.closeOwnedSource(source))
	}
	if prepareErr != nil || sourceCloseErr != nil {
		cleanupErr := error(nil)
		if temporary != nil {
			cleanupErr = disk.removeTemporaryLocked(temporary)
		}
		return false, errors.Join(prepareErr, sourceCloseErr, cleanupErr)
	}
	return disk.publishAtomicFileLocked(temporary, target)
}

func (disk *Local) prepareAtomicFileLocked(
	target string,
	contents io.Reader,
	mode os.FileMode,
	copyMessage string,
) (*localAtomicTemporary, error) {
	temporary, err := disk.createAtomicTemporaryLocked(target)
	if err != nil {
		return nil, err
	}
	fail := func(operationErr error, closeAttempted bool) (*localAtomicTemporary, error) {
		closeErr := error(nil)
		if closeAttempted {
			// 关闭实现可能在返回错误前后关闭句柄，直接再关闭一次仅用于确保可清理。
			_ = temporary.file.Close()
		} else {
			closeErr = disk.operations.closeTemporary(temporary.file)
			if closeErr != nil {
				_ = temporary.file.Close()
			}
		}
		return temporary, errors.Join(
			operationErr,
			wrapOperationError("关闭临时文件失败", closeErr),
			disk.removeTemporaryLocked(temporary),
		)
	}
	if _, err = disk.operations.copy(temporary.file, contents); err != nil {
		return fail(wrapOperationError(copyMessage, err), false)
	}
	if err = disk.validateAtomicTemporaryLocked(temporary); err != nil {
		return fail(err, false)
	}
	if err = disk.operations.chmodTemporary(temporary.file, mode); err != nil {
		return fail(fmt.Errorf("设置临时文件权限失败: %w", err), false)
	}
	if err = disk.operations.syncFile(temporary.file); err != nil {
		return fail(fmt.Errorf("同步临时文件失败: %w", err), false)
	}
	if err = disk.operations.closeTemporary(temporary.file); err != nil {
		return fail(fmt.Errorf("关闭临时文件失败: %w", err), true)
	}
	return temporary, nil
}

func (disk *Local) createAtomicTemporaryLocked(target string) (*localAtomicTemporary, error) {
	if err := disk.validateAtomicParentDirectoryLocked(target); err != nil {
		return nil, err
	}
	parent := path.Dir(target)
	if parent == "." {
		parent = ""
	}
	for attempt := 0; attempt < localTemporaryAttempts; attempt++ {
		randomBytes := make([]byte, 16)
		if _, err := rand.Read(randomBytes); err != nil {
			return nil, fmt.Errorf("生成临时文件名失败: %w", err)
		}
		temporaryName := localTemporaryPrefix + hex.EncodeToString(randomBytes)
		if parent != "" {
			temporaryName = path.Join(parent, temporaryName)
		}
		temporary, err := disk.root.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			information, statErr := temporary.Stat()
			if statErr != nil {
				// Without the descriptor identity, never unlink an uncertain path.
				return nil, errors.Join(fmt.Errorf("读取临时文件身份失败: %w", statErr), temporary.Close())
			}
			return &localAtomicTemporary{name: temporaryName, file: temporary, information: information}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("创建同目录临时文件失败: %w", err)
		}
	}
	return nil, fmt.Errorf("创建同目录临时文件失败: 随机名称连续冲突 %d 次", localTemporaryAttempts)
}

func (disk *Local) publishAtomicFileLocked(temporary *localAtomicTemporary, target string) (bool, error) {
	if err := disk.validateAtomicTemporaryLocked(temporary); err != nil {
		return false, err
	}
	if err := disk.operations.replace(disk.root, temporary.name, target); err != nil {
		return false, errors.Join(
			fmt.Errorf("原子替换目标失败: %w", err),
			disk.removeTemporaryLocked(temporary),
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

func (disk *Local) validateAtomicTemporaryLocked(temporary *localAtomicTemporary) error {
	if temporary == nil || temporary.information == nil {
		return errLocalTemporaryChanged
	}
	if err := disk.validateAtomicParentDirectoryLocked(temporary.name); err != nil {
		return err
	}
	information, err := disk.root.Lstat(temporary.name)
	if err != nil {
		return errors.Join(errLocalTemporaryChanged, err)
	}
	if !information.Mode().IsRegular() || !os.SameFile(temporary.information, information) {
		return errLocalTemporaryChanged
	}
	return nil
}

func (disk *Local) removeTemporaryLocked(temporary *localAtomicTemporary) error {
	if temporary == nil {
		return nil
	}
	if err := disk.validateAtomicTemporaryLocked(temporary); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		// Do not delete a replacement directory entry, including on failure paths.
		return err
	}
	err := disk.root.Remove(temporary.name)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("清理临时文件失败: %w", err)
}
