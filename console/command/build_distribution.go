package command

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/winfile"
)

type distributionCompiler func(context.Context, string, string, buildTarget) error

const (
	distributionGuardPath      = "dist/.build.guard"
	distributionMarkerPath     = "dist/.build.owner"
	distributionLegacyLockPath = "dist/.build.lock"
)

func buildDistribution(ctx context.Context, base string, target buildTarget, compile distributionCompiler) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	base, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := ensureDistributionDirectory(root, "dist"); err != nil {
		return err
	}
	// 使用稳定文件上的内核锁覆盖编译和产物发布，进程退出时由系统释放。
	err = winfile.TryWithGuard(ctx, filepath.Join(base, distributionGuardPath), func() error {
		return withLegacyBuildMarker(root, func() error {
			return buildLockedDistribution(ctx, base, root, target, compile)
		})
	})
	if errors.Is(err, winfile.ErrGuardInUse) {
		return fmt.Errorf("另一个构建正在执行: %w", err)
	}
	return err
}

// withLegacyBuildMarker 仅在持有新版内核锁时使用，通过原子的兼容标记阻止旧版并发进入。
func withLegacyBuildMarker(root *os.Root, operation func() error) (result error) {
	owner, err := root.Lstat(distributionMarkerPath)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := root.OpenFile(distributionMarkerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if createErr != nil {
			return createErr
		}
		owner, err = file.Stat()
		err = errors.Join(err, file.Close())
	}
	if err != nil {
		return err
	}
	if !owner.Mode().IsRegular() {
		return winfile.ErrUnsafeGuard
	}
	// 同一文件的硬链接可在崩溃后辨认，且创建时不会覆盖旧进程已经取得的锁。
	if err := root.Link(distributionMarkerPath, distributionLegacyLockPath); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("无法发布旧版兼容构建锁: %w", err)
	}
	current, err := root.Lstat(distributionLegacyLockPath)
	if err != nil {
		return err
	}
	if !os.SameFile(owner, current) {
		return fmt.Errorf("发现旧版构建锁 %s，请先停止旧版构建，再删除该文件并重试", distributionLegacyLockPath)
	}
	defer func() {
		marker, err := root.Lstat(distributionLegacyLockPath)
		if err == nil && os.SameFile(owner, marker) {
			err = root.Remove(distributionLegacyLockPath)
		}
		if !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	return operation()
}

func buildLockedDistribution(ctx context.Context, base string, root *os.Root, target buildTarget, compile distributionCompiler) (returnErr error) {
	stageName := filepath.Join("dist", ".stage-"+rand.Text())
	if err := root.Mkdir(stageName, 0755); err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.RemoveAll(stageName)) }()
	stage, err := root.OpenRoot(stageName)
	if err != nil {
		return err
	}
	if err := compile(ctx, base, filepath.Join(base, stageName, target.binary()), target); err != nil {
		_ = stage.Close()
		return err
	}
	info, err := stage.Lstat(target.binary())
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("构建产物必须是普通文件")
	}
	if err == nil {
		err = copyDistributionResources(ctx, root, stage)
	}
	if err == nil && target.os == "linux" {
		err = writeDockerDistribution(stage, target)
	}
	err = errors.Join(err, stage.Close(), ctx.Err())
	if err != nil {
		return err
	}
	return replaceDistribution(root, stageName, filepath.Join("dist", target.directory()))
}

func ensureDistributionDirectory(root *os.Root, path string) error {
	info, err := root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return root.MkdirAll(path, 0755)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("目录 %q 不能是链接或普通文件", path)
	}
	return nil
}

func replaceDistribution(root *os.Root, stage, target string) error {
	info, err := root.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return root.Rename(stage, target)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("拒绝覆盖非普通发布目录 %q", target)
	}
	backup := target + ".previous-" + rand.Text()
	if err := root.Rename(target, backup); err != nil {
		return err
	}
	if err := root.Rename(stage, target); err != nil {
		return errors.Join(err, root.Rename(backup, target))
	}
	return root.RemoveAll(backup)
}

func copyDistributionResources(ctx context.Context, source, target *os.Root) error {
	for _, name := range []string{"config", "app"} {
		info, err := source.Lstat(name)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("项目 %s 必须是普通目录", name)
		}
		if err := target.MkdirAll(name, 0755); err != nil {
			return err
		}
	}
	if err := copyDistributionTree(ctx, source, target, "config", true); err != nil {
		return err
	}
	entries, err := fs.ReadDir(source.FS(), "app")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("应用目录不能包含链接: %s", entry.Name())
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join("app", entry.Name())
		if entry.Name() == "lang" || entry.Name() == "view" {
			if err := copyDistributionTree(ctx, source, target, path, entry.Name() == "lang"); err != nil {
				return err
			}
			continue
		}
		if err := target.MkdirAll(path, 0755); err != nil {
			return err
		}
		for _, resource := range []string{"config", "lang", "view"} {
			if err := copyDistributionTree(ctx, source, target, filepath.Join(path, resource), resource != "view"); err != nil {
				return err
			}
		}
	}
	if err := copyDistributionTree(ctx, source, target, "public", false); err != nil {
		return err
	}
	if err := copyDistributionTree(ctx, source, target, "view", false); err != nil {
		return err
	}
	return copyConfiguredDistributionResources(ctx, source, target)
}

func copyDistributionTree(ctx context.Context, source, target *os.Root, directory string, jsonOnly bool) error {
	if _, err := source.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return fs.WalkDir(source.FS(), filepath.ToSlash(directory), func(path string, entry fs.DirEntry, walkErr error) error {
		if err := errors.Join(walkErr, ctx.Err()); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("发布资源不能是链接: %s", path)
		}
		if entry.Name() == ".git" || entry.Name() == ".env" || strings.HasPrefix(entry.Name(), ".env.") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return target.MkdirAll(path, 0755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("发布资源必须是普通文件: %s", path)
		}
		if jsonOnly && !strings.EqualFold(filepath.Ext(path), ".json") {
			return nil
		}
		return copyDistributionFile(source, target, path)
	})
}

func copyDistributionFile(source, target *os.Root, path string) (returnErr error) {
	input, err := source.Open(path)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, input.Close()) }()
	if err := target.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	mode := fs.FileMode(0644)
	flags := os.O_CREATE | os.O_TRUNC | os.O_WRONLY
	privateConfig := isDistributionConfig(path)
	if privateConfig {
		mode = 0600
		// Tighten existing files before truncating or copying any secret content.
		flags &^= os.O_TRUNC
	}
	output, err := target.OpenFile(path, flags, mode)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, output.Close()) }()
	if privateConfig {
		if err := output.Chmod(0600); err != nil {
			return err
		}
		if err := output.Truncate(0); err != nil {
			return err
		}
	}
	_, err = io.Copy(output, input)
	return err
}

// Match the destination path so a configured-resource copy cannot later widen
// the mode of configuration that was already included by the standard walk.
func isDistributionConfig(path string) bool {
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		return false
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	return len(parts) >= 2 && strings.EqualFold(parts[0], "config") ||
		len(parts) >= 4 && strings.EqualFold(parts[0], "app") &&
			parts[1] != "lang" && parts[1] != "view" && strings.EqualFold(parts[2], "config")
}
