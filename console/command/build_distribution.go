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
)

type distributionCompiler func(context.Context, string, string, buildTarget) error

func buildDistribution(ctx context.Context, base string, target buildTarget, compile distributionCompiler) (returnErr error) {
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
	// 锁覆盖整个项目构建过程，避免并发平台构建互相改写静态业务清单。
	lock, err := root.OpenFile("dist/.build.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("无法获得构建锁，请确认没有其他构建进程: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close(), root.Remove("dist/.build.lock")) }()
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
	output, err := target.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, output.Close()) }()
	_, err = io.Copy(output, input)
	return err
}
