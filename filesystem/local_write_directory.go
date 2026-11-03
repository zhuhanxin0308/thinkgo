package filesystem

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
)

func (disk *Local) ensureAtomicParentDirectoryLocked(target, visibility string) error {
	return disk.checkAtomicParentDirectoryLocked(target, disk.configuration.directoryMode(visibility), true)
}

func (disk *Local) validateAtomicParentDirectoryLocked(target string) error {
	return disk.checkAtomicParentDirectoryLocked(target, 0, false)
}

// checkAtomicParentDirectoryLocked starts at the pinned os.Root, then validates
// each component before descending or creating anything in it. Under Unix DAC,
// another UID cannot replace an entry after its containing directory has passed
// this check. Temporary-file identity checks are additional corruption checks;
// they do not replace this directory trust requirement.
func (disk *Local) checkAtomicParentDirectoryLocked(target string, mode os.FileMode, create bool) error {
	rootInformation, err := disk.root.Stat(".")
	if err != nil {
		return errors.Join(ErrUnsafeLocalWriteDirectory, err)
	}
	if err := validateAtomicDirectoryInformation(rootInformation); err != nil {
		return err
	}
	parent := path.Dir(target)
	if parent == "." {
		return nil
	}
	current := ""
	for _, component := range strings.Split(parent, "/") {
		current = path.Join(current, component)
		information, err := disk.root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := validateLocalDirectoryMode(mode); err != nil {
				return err
			}
			if err := disk.root.Mkdir(current, mode); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("创建原子写入父目录失败: %w", err)
			}
			information, err = disk.root.Lstat(current)
		}
		if err != nil {
			return errors.Join(ErrUnsafeLocalWriteDirectory, err)
		}
		if err := validateAtomicDirectoryInformation(information); err != nil {
			return err
		}
	}
	return nil
}

func validateAtomicDirectoryInformation(information os.FileInfo) error {
	if information.Mode()&os.ModeSymlink != 0 {
		return errors.Join(ErrUnsafeLocalWriteDirectory, ErrSymbolicLink)
	}
	if !information.IsDir() {
		return ErrUnsafeLocalWriteDirectory
	}
	return validateLocalDirectoryOwnership(information)
}
