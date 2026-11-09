package command

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrControllerDiscoveryRollbackConflict 表示回滚发现外部变更，相关版本保留供恢复。
var ErrControllerDiscoveryRollbackConflict = errors.New("控制器发现回滚冲突")

func rollbackControllerDiscoverySources(root *os.Root, staged []stagedControllerDiscoverySource) error {
	var rollbackErr error
	for index := len(staged) - 1; index >= 0; index-- {
		rollbackErr = errors.Join(rollbackErr, rollbackControllerDiscoverySource(root, &staged[index]))
	}
	return rollbackErr
}

func rollbackControllerDiscoverySource(root *os.Root, current *stagedControllerDiscoverySource) error {
	if !current.published {
		return restoreControllerDiscoveryBackup(root, current)
	}
	if err := verifyPublishedControllerDiscoverySource(root, current.relativePath, current); err != nil {
		return controllerDiscoveryRollbackConflict(current, "", err)
	}
	rollbackPath, err := unusedControllerDiscoveryAuxiliaryPath(root, current.relativePath, "rollback")
	if err != nil {
		return err
	}
	// 先捕获当前路径，再核验被移走的实际对象，避免在检查后直接删除外部替换版本。
	if err := renameControllerDiscoveryPath(root, current.relativePath, rollbackPath); err != nil {
		return controllerDiscoveryRollbackConflict(current, "", err)
	}
	if err := verifyPublishedControllerDiscoverySource(root, rollbackPath, current); err != nil {
		// 恢复也不得覆盖捕获期间新出现的路径；失败时保留两个版本并报告各自位置。
		restoreErr := restoreControllerDiscoveryPathWithoutReplace(root, rollbackPath, current.relativePath)
		return controllerDiscoveryRollbackConflict(current, rollbackPath, errors.Join(err, restoreErr))
	}
	if err := restoreControllerDiscoveryBackup(root, current); err != nil {
		return controllerDiscoveryRollbackConflict(current, rollbackPath, err)
	}
	current.published = false
	if err := verifyPublishedControllerDiscoverySource(root, rollbackPath, current); err != nil {
		return controllerDiscoveryRollbackConflict(current, rollbackPath, err)
	}
	if err := root.Remove(rollbackPath); err != nil {
		return fmt.Errorf("清理控制器发现回滚暂存 %q 失败: %w", rollbackPath, err)
	}
	return nil
}

// restoreControllerDiscoveryPathWithoutReplace 通过创建硬链接原子拒绝已存在的目标。
// 链接失败时源版本仍在；不支持该能力的文件系统保留备份并返回错误，不退化为覆盖。
func restoreControllerDiscoveryPathWithoutReplace(root *os.Root, source, target string) error {
	if err := root.Link(source, target); err != nil {
		return err
	}
	return root.Remove(source)
}

func restoreControllerDiscoveryBackup(root *os.Root, current *stagedControllerDiscoverySource) error {
	if !current.backupMoved {
		return nil
	}
	if err := restoreControllerDiscoveryPathWithoutReplace(root, current.backupPath, current.relativePath); err != nil {
		return controllerDiscoveryRollbackConflict(current, "", err)
	}
	current.backupMoved = false
	current.backupPath = ""
	return nil
}

func verifyPublishedControllerDiscoverySource(root *os.Root, path string, current *stagedControllerDiscoverySource) (returnErr error) {
	information, err := root.Lstat(path)
	if err != nil {
		return err
	}
	if current.publishedInfo == nil || !information.Mode().IsRegular() || !os.SameFile(current.publishedInfo, information) {
		return errors.New("已发布文件身份发生变化")
	}
	file, err := root.Open(path)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(information, opened) || !opened.Mode().IsRegular() {
		return errors.New("已发布文件在打开期间被替换")
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(len(current.publishedContent))+1))
	if err != nil {
		return err
	}
	latest, err := root.Lstat(path)
	if err != nil {
		return err
	}
	if !latest.Mode().IsRegular() || !os.SameFile(opened, latest) ||
		latest.Mode() != current.publishedInfo.Mode() || !latest.ModTime().Equal(current.publishedInfo.ModTime()) ||
		!bytes.Equal(content, current.publishedContent) {
		return errors.New("已发布文件内容、权限或身份已被外部修改")
	}
	return nil
}

func controllerDiscoveryRollbackConflict(current *stagedControllerDiscoverySource, captured string, cause error) error {
	detail := fmt.Sprintf("目标 %q", current.relativePath)
	if current.backupPath != "" {
		detail += fmt.Sprintf("，旧版本备份保留在 %q", current.backupPath)
	}
	if captured != "" {
		detail += fmt.Sprintf("，捕获版本恢复失败时保留在 %q", captured)
	}
	return fmt.Errorf("%w: %s: %w", ErrControllerDiscoveryRollbackConflict, detail, cause)
}
