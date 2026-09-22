package command

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/framework"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

// Clear 默认清理当前应用缓存，显式选项才允许清理日志或指定目录。
type Clear struct {
	console.Command
}

func (c *Clear) Configure() {
	c.Signature = "clear"
	c.Description = "Clear application cache"
	c.AddArgument("app", "app name .", false)
	c.AddOption("path", "d", "path to clear", "")
	c.AddBoolOption("cache", "c", "clear cache file")
	c.AddBoolOption("log", "l", "clear log file")
	c.AddBoolOption("dir", "r", "clear empty dir")
	c.AddBoolOption("expire", "e", "clear cache file if cache has expired")
}

func (c *Clear) Execute(input *console.Input, output *console.Output) error {
	if input == nil {
		return fmt.Errorf("命令输入不能为空")
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	if c.App == nil {
		return framework.ErrNilApplication
	}
	if strings.TrimSpace(c.App.BasePath) == "" {
		return fmt.Errorf("failed to clear runtime: app base path is empty")
	}
	applicationName := strings.TrimSpace(input.GetArgument(0))
	// 兼容未经 Console 解析、以选项开头的直接调用。
	if strings.HasPrefix(applicationName, "-") {
		applicationName = ""
	}
	applications, err := compiledApplicationsForCommand(c.App, applicationName, false)
	if err != nil {
		return err
	}
	application := applications[0].application
	target, cacheSelected, err := clearTargetPath(application, input)
	if err != nil {
		return err
	}
	removeDirectories := input.GetOption("dir") == "true"
	if cacheSelected {
		err = clearApplicationCache(application, target, removeDirectories, input.GetOption("expire") == "true")
	} else {
		err = clearRuntimeFiles(target, removeDirectories)
	}
	if err != nil {
		return fmt.Errorf("clear runtime path %q: %w", target, err)
	}
	output.Success("Clear Successed")
	return nil
}

func clearTargetPath(app *framework.App, input *console.Input) (string, bool, error) {
	basePath, err := filepath.Abs(filepath.Clean(app.BasePath))
	if err != nil {
		return "", false, fmt.Errorf("解析项目根目录失败: %w", err)
	}
	cacheSelected := input.GetOption("cache") == "true" ||
		(input.GetOption("log") != "true" && strings.TrimSpace(input.GetOption("path")) == "")
	target := ""
	switch {
	case cacheSelected:
		target = app.RuntimeCachePath()
	case input.GetOption("log") == "true":
		target = app.RuntimeLogPath()
	case strings.TrimSpace(input.GetOption("path")) != "":
		target = strings.TrimSpace(input.GetOption("path"))
		if !filepath.IsAbs(target) {
			target = filepath.Join(basePath, target)
		}
	}
	target, err = filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", false, fmt.Errorf("解析清理目录失败: %w", err)
	}
	if filepath.Clean(target) == filepath.Clean(basePath) || filepath.Dir(target) == target {
		return "", false, fmt.Errorf("拒绝清理项目根目录或文件系统根目录: %s", target)
	}
	return target, cacheSelected, nil
}

func clearApplicationCache(app *framework.App, target string, removeDirectories, expireOnly bool) error {
	if !expireOnly {
		backend, err := resolveApplicationCache(app)
		if err != nil {
			return err
		}
		// cache 类型的 Session 随所属缓存清理；后端失败时不再触碰本地文件。
		if err := backend.Flush(); err != nil {
			return err
		}
	}
	information, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
		return fmt.Errorf("清理目标必须是真实目录")
	}
	files, err := cacheDriver.NewFile(target)
	if err != nil {
		return err
	}
	if expireOnly {
		err = files.ClearExpired()
	} else {
		err = files.Clear()
	}
	if err != nil || !removeDirectories {
		return err
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		return err
	}
	return errors.Join(clearEmptyCacheDirectories(root, "."), root.Close())
}

// clearEmptyCacheDirectories 仅移除空子目录，不删除同步文件或递归清理未知数据。
func clearEmptyCacheDirectories(root *os.Root, relative string) error {
	directory, err := root.Open(relative)
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	if err := errors.Join(readErr, directory.Close()); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			path := filepath.Join(relative, entry.Name())
			if err := clearEmptyCacheDirectories(root, path); err != nil {
				return err
			}
			if err := removeEmptyRuntimeDirectory(root, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func clearRuntimeFiles(target string, removeDirectories bool) (returnErr error) {
	information, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
		return fmt.Errorf("清理目标必须是真实目录")
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	return clearRuntimeDirectory(root, ".", removeDirectories)
}

func clearRuntimeDirectory(root *os.Root, relative string, removeDirectories bool) error {
	directory, err := root.Open(relative)
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if combined := errors.Join(readErr, closeErr); combined != nil {
		return combined
	}
	var clearErr error
	for _, entry := range entries {
		if entry.Name() == ".gitignore" {
			continue
		}
		path := filepath.Join(relative, entry.Name())
		information, statErr := root.Lstat(path)
		if statErr != nil {
			clearErr = errors.Join(clearErr, statErr)
			continue
		}
		if information.IsDir() {
			clearErr = errors.Join(clearErr, clearRuntimeDirectory(root, path, removeDirectories))
			if removeDirectories {
				clearErr = errors.Join(clearErr, removeEmptyRuntimeDirectory(root, path))
			}
			continue
		}
		if information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 {
			if removeErr := root.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				clearErr = errors.Join(clearErr, removeErr)
			}
		}
	}
	return clearErr
}

func removeEmptyRuntimeDirectory(root *os.Root, relative string) error {
	directory, err := root.Open(relative)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if combined := errors.Join(readErr, closeErr); combined != nil {
		return combined
	}
	if len(entries) != 0 {
		return nil
	}
	if err := root.Remove(relative); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
