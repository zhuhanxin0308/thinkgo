package command

import (
	"errors"
	"fmt"
	"os"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
)

// refreshDiscovery 在最终校验前保留全部发现文件的发布身份和旧版本备份。
func (transaction *applicationBuildTransaction) refreshDiscovery(app *framework.App) (returnErr error) {
	controllerDiscoveryTransactionMu.Lock()
	defer controllerDiscoveryTransactionMu.Unlock()
	sources, err := nativeApplicationDiscoverySources(app)
	if err != nil {
		return err
	}
	transaction.generatedSources, err = stageControllerDiscoverySources(transaction.root, sources)
	defer func() {
		returnErr = errors.Join(returnErr, cleanupControllerDiscoveryTemporaryFiles(transaction.root, transaction.generatedSources))
	}()
	if err != nil {
		return err
	}
	if err := publishControllerDiscoverySources(transaction.root, transaction.generatedSources); err != nil {
		return err
	}
	return checkControllerDiscovery(app)
}

func (transaction *applicationBuildTransaction) rollback() error {
	if transaction == nil || transaction.root == nil {
		return nil
	}
	rollbackErr := rollbackControllerDiscoverySources(transaction.root, transaction.generatedSources)
	for index := len(transaction.createdFiles) - 1; index >= 0; index-- {
		current := &transaction.createdFiles[index]
		err := rollbackControllerDiscoverySource(transaction.root, current)
		if errors.Is(err, ErrControllerDiscoveryRollbackConflict) {
			// 外部版本保留原位，本次生成内容另存恢复文件，避免冲突后只剩一方材料。
			backup, backupErr := newControllerDiscoveryAuxiliaryPath(current.relativePath, "original")
			if backupErr == nil {
				backupErr = writeControllerDiscoveryStagedSourceFile(transaction.root, backup, current.publishedContent, current.publishedInfo.Mode().Perm())
			}
			if backupErr == nil {
				err = fmt.Errorf("%w；本次生成内容保留在 %q", err, backup)
			} else {
				err = errors.Join(err, backupErr)
			}
		}
		rollbackErr = errors.Join(rollbackErr, err)
	}
	for index := len(transaction.createdDirectories) - 1; index >= 0; index-- {
		directory := transaction.createdDirectories[index]
		information, err := transaction.root.Lstat(directory.relativePath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil && (!information.IsDir() || !os.SameFile(information, directory.information)) {
			err = fmt.Errorf("%w: 应用目录 %q 身份变化", ErrControllerDiscoveryRollbackConflict, directory.relativePath)
		}
		if err == nil {
			err = transaction.root.Remove(directory.relativePath)
		}
		if err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("回滚应用目录 %q 失败: %w", directory.relativePath, err))
		}
	}
	return rollbackErr
}
