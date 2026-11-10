package command

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	controllerDiscoveryAuxiliaryRandomBytes = 16
	controllerDiscoveryAuxiliaryAttempts    = 8
)

var controllerDiscoveryTransactionMu sync.Mutex

var writeControllerDiscoveryStagedSource = writeControllerDiscoveryStagedSourceFile

var renameControllerDiscoveryPath = func(root *os.Root, oldPath, newPath string) error {
	return root.Rename(oldPath, newPath)
}

var linkControllerDiscoveryPublication = func(root *os.Root, source, target string) error {
	return root.Link(source, target)
}

// ErrControllerDiscoveryPublishConflict 表示外部修改使本次发布失去目标所有权。
var ErrControllerDiscoveryPublishConflict = errors.New("控制器发现发布冲突")

type stagedControllerDiscoverySource struct {
	relativePath     string
	temporaryPath    string
	backupPath       string
	original         []byte
	originalInfo     os.FileInfo
	existed          bool
	backupMoved      bool
	published        bool
	publishedInfo    os.FileInfo
	publishedContent []byte
}

// replaceGeneratedSourceBatch 先完成全部文件的临时写入，再按批次发布；
// 任一目标替换失败时会逆序恢复已经发布的旧版本。
func replaceGeneratedSourceBatch(basePath string, sources []generatedApplicationSource) (returnErr error) {
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return fmt.Errorf("打开项目根目录失败: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	staged, err := stageControllerDiscoverySources(root, sources)
	if err != nil {
		return errors.Join(err, cleanupControllerDiscoveryTemporaryFiles(root, staged))
	}
	if err = publishControllerDiscoverySources(root, staged); err != nil {
		rollbackErr := rollbackControllerDiscoverySources(root, staged)
		cleanupErr := cleanupControllerDiscoveryTemporaryFiles(root, staged)
		return errors.Join(err, rollbackErr, cleanupErr)
	}
	return cleanupControllerDiscoveryBackups(root, staged)
}

func stageControllerDiscoverySources(root *os.Root, sources []generatedApplicationSource) ([]stagedControllerDiscoverySource, error) {
	staged := make([]stagedControllerDiscoverySource, 0, len(sources))
	seen := make(map[string]struct{}, len(sources))
	for _, generated := range sources {
		relativePath, err := validateControllerDiscoveryRelativePath(generated.relativePath)
		if err != nil {
			return staged, err
		}
		if _, exists := seen[relativePath]; exists {
			return staged, fmt.Errorf("控制器发现批次包含重复目标 %q", relativePath)
		}
		seen[relativePath] = struct{}{}
		current := stagedControllerDiscoverySource{relativePath: relativePath}
		information, statErr := root.Lstat(relativePath)
		switch {
		case statErr == nil:
			if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
				return staged, fmt.Errorf("控制器发现目标 %q 必须是普通文件", relativePath)
			}
			content, readErr := root.ReadFile(relativePath)
			if readErr != nil {
				return staged, fmt.Errorf("读取旧控制器发现文件 %q 失败: %w", relativePath, readErr)
			}
			if bytes.Equal(content, generated.source) {
				continue
			}
			current.existed = true
			current.original = content
			current.originalInfo = information
		case errors.Is(statErr, os.ErrNotExist):
		default:
			return staged, fmt.Errorf("检查控制器发现目标 %q 失败: %w", relativePath, statErr)
		}
		if err = root.MkdirAll(filepath.Dir(relativePath), 0o755); err != nil {
			return staged, fmt.Errorf("创建控制器发现目录失败: %w", err)
		}
		permission := os.FileMode(0o644)
		if information != nil {
			permission = information.Mode().Perm()
		}
		current.temporaryPath, err = newControllerDiscoveryAuxiliaryPath(relativePath, "tmp")
		if err != nil {
			return staged, err
		}
		current.publishedContent = append([]byte(nil), generated.source...)
		if err = writeControllerDiscoveryStagedSource(root, current.temporaryPath, current.publishedContent, permission); err != nil {
			return staged, fmt.Errorf("暂存控制器发现文件 %q 失败: %w", relativePath, err)
		}
		current.publishedInfo, err = root.Lstat(current.temporaryPath)
		staged = append(staged, current)
		if err != nil {
			return staged, fmt.Errorf("记录控制器发现暂存文件身份失败: %w", err)
		}
	}
	return staged, nil
}

func publishControllerDiscoverySources(root *os.Root, staged []stagedControllerDiscoverySource) error {
	for index := range staged {
		current := &staged[index]
		if err := verifyControllerDiscoveryTarget(root, current); err != nil {
			return err
		}
		if current.existed {
			backupPath, err := unusedControllerDiscoveryAuxiliaryPath(root, current.relativePath, "backup")
			if err != nil {
				return err
			}
			current.backupPath = backupPath
			if err = renameControllerDiscoveryPath(root, current.relativePath, current.backupPath); err != nil {
				return fmt.Errorf("备份控制器发现文件 %q 失败: %w", current.relativePath, err)
			}
			current.backupMoved = true
			// 核验实际捕获的对象，覆盖预检之后、移走目标之前的外部保存窗口。
			if err := verifyOriginalControllerDiscoverySource(root, current.backupPath, current); err != nil {
				return fmt.Errorf("%w: 捕获目标 %q 后发现外部修改: %w", ErrControllerDiscoveryPublishConflict, current.relativePath, err)
			}
		}
		// 最终发布原子拒绝已存在的路径，既保护首次生成，也保护备份之后的新目标。
		if err := linkControllerDiscoveryPublication(root, current.temporaryPath, current.relativePath); err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%w: 目标 %q 已被创建: %w", ErrControllerDiscoveryPublishConflict, current.relativePath, err)
			}
			return fmt.Errorf("发布控制器发现文件 %q 失败: %w", current.relativePath, err)
		}
		current.published = true
		if err := root.Remove(current.temporaryPath); err != nil {
			return fmt.Errorf("清理已发布控制器发现临时文件 %q 失败: %w", current.temporaryPath, err)
		}
		current.temporaryPath = ""
	}
	return nil
}

func verifyControllerDiscoveryTarget(root *os.Root, staged *stagedControllerDiscoverySource) error {
	_, err := root.Lstat(staged.relativePath)
	if !staged.existed {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("重新检查控制器发现目标 %q 失败: %w", staged.relativePath, err)
		}
		return fmt.Errorf("%w: 目标 %q 在批次发布期间被创建", ErrControllerDiscoveryPublishConflict, staged.relativePath)
	}
	if err != nil {
		return fmt.Errorf("控制器发现目标 %q 在批次发布期间不可用: %w", staged.relativePath, err)
	}
	if err := verifyOriginalControllerDiscoverySource(root, staged.relativePath, staged); err != nil {
		return fmt.Errorf("%w: 目标 %q 在批次发布期间已变化: %w", ErrControllerDiscoveryPublishConflict, staged.relativePath, err)
	}
	return nil
}

func cleanupControllerDiscoveryTemporaryFiles(root *os.Root, staged []stagedControllerDiscoverySource) error {
	var cleanupErr error
	for index := range staged {
		if staged[index].temporaryPath == "" {
			continue
		}
		if err := root.Remove(staged[index].temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("清理控制器发现临时文件 %q 失败: %w", staged[index].temporaryPath, err))
		}
	}
	return cleanupErr
}

func cleanupControllerDiscoveryBackups(root *os.Root, staged []stagedControllerDiscoverySource) error {
	var cleanupErr error
	for index := range staged {
		if staged[index].backupPath == "" {
			continue
		}
		if err := verifyOriginalControllerDiscoverySource(root, staged[index].backupPath, &staged[index]); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("%w: 备份 %q 已变化并保留: %w", ErrControllerDiscoveryPublishConflict, staged[index].backupPath, err))
			continue
		}
		if err := root.Remove(staged[index].backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("清理控制器发现备份文件 %q 失败: %w", staged[index].backupPath, err))
			continue
		}
		staged[index].backupMoved = false
		staged[index].backupPath = ""
	}
	return cleanupErr
}

func writeControllerDiscoveryStagedSourceFile(root *os.Root, relativePath string, source []byte, permission os.FileMode) (returnErr error) {
	file, err := root.OpenFile(relativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permission)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			returnErr = errors.Join(returnErr, root.Remove(relativePath))
		}
	}()
	written, writeErr := file.Write(source)
	if writeErr == nil && written != len(source) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return errors.Join(writeErr, file.Close())
	}
	if err = file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err = file.Close(); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

func validateControllerDiscoveryRelativePath(relativePath string) (string, error) {
	if relativePath == "" || filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("控制器发现目标路径 %q 非法", relativePath)
	}
	cleaned := filepath.Clean(relativePath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("控制器发现目标路径 %q 逃逸项目根目录", relativePath)
	}
	return cleaned, nil
}

func unusedControllerDiscoveryAuxiliaryPath(root *os.Root, relativePath, kind string) (string, error) {
	for attempt := 0; attempt < controllerDiscoveryAuxiliaryAttempts; attempt++ {
		candidate, err := newControllerDiscoveryAuxiliaryPath(relativePath, kind)
		if err != nil {
			return "", err
		}
		_, err = root.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("检查控制器发现辅助路径 %q 失败: %w", candidate, err)
		}
	}
	return "", fmt.Errorf("无法为控制器发现文件 %q 分配唯一辅助路径", relativePath)
}

func newControllerDiscoveryAuxiliaryPath(relativePath, kind string) (string, error) {
	random := make([]byte, controllerDiscoveryAuxiliaryRandomBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("生成控制器发现辅助文件名失败: %w", err)
	}
	return fmt.Sprintf("%s.discovery-%s-%s", relativePath, kind, hex.EncodeToString(random)), nil
}
