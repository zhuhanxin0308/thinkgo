package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func copyConfiguredDistributionResources(ctx context.Context, source, target *os.Root) error {
	return fs.WalkDir(source.FS(), "app", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "app" {
			return copyConfiguredResourceDirectory(ctx, source, target, "config", "app")
		}
		parts := strings.Split(path, "/")
		if entry.IsDir() && len(parts) == 2 {
			if err := copyConfiguredResourceDirectory(ctx, source, target, filepath.Join(path, "config"), path); err != nil {
				return err
			}
			return fs.SkipDir
		}
		return nil
	})
}

func copyConfiguredResourceDirectory(ctx context.Context, source, target *os.Root, directory, application string) error {
	for _, configName := range []string{"app", "view"} {
		content, err := source.ReadFile(filepath.Join(directory, configName+".json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		configuration := map[string]json.RawMessage{}
		if err := json.Unmarshal(content, &configuration); err != nil {
			return fmt.Errorf("配置 %s/%s.json 不是有效的 JSON 对象: %w", directory, configName, err)
		}
		keys := []string{"public_path", "exception_tmpl"}
		if configName == "view" {
			keys = []string{"view_path", "view_dir_name"}
		}
		for _, key := range keys {
			value, exists := configuration[key]
			if !exists {
				continue
			}
			var path string
			if err := json.Unmarshal(value, &path); err != nil {
				return fmt.Errorf("配置 %s.%s 必须是路径字符串", configName, key)
			}
			if strings.TrimSpace(path) == "" {
				continue
			}
			if key == "view_dir_name" {
				if filepath.Base(path) != path || path == "." || path == ".." {
					return fmt.Errorf("模板目录名必须是单个目录")
				}
				if application == "app" {
					if err := validateDistributionResourcePath(path); err != nil {
						return err
					}
					if err := copyDistributionTree(ctx, source, target, path, false); err != nil {
						return err
					}
					entries, err := fs.ReadDir(source.FS(), "app")
					if err != nil {
						return err
					}
					for _, entry := range entries {
						if entry.IsDir() {
							if err := copyDistributionTree(ctx, source, target, filepath.Join("app", entry.Name(), path), false); err != nil {
								return err
							}
						}
					}
				}
				path = filepath.Join(application, path)
			}
			path = filepath.Clean(filepath.FromSlash(path))
			if key == "view_path" && (path == "view" || path == "app" || filepath.ToSlash(path) == "app/view") {
				path = filepath.Join(application, "view")
			}
			if err := validateDistributionResourcePath(path); err != nil {
				return fmt.Errorf("配置 %s.%s: %w", configName, key, err)
			}
			if err := copyDistributionTree(ctx, source, target, path, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateDistributionResourcePath(path string) error {
	if !filepath.IsLocal(path) || path == "." || strings.Contains(path, "\\") && filepath.Separator != '\\' {
		return fmt.Errorf("发布资源路径必须位于项目目录内: %q", path)
	}
	first := strings.Split(filepath.ToSlash(path), "/")[0]
	switch strings.ToLower(first) {
	case "dist", "runtime", "framework", "vendor", "cmd", "config", "go.mod", "go.sum":
		return fmt.Errorf("发布资源路径不能指向 %q", first)
	}
	if strings.HasPrefix(first, ".") || path == "app" {
		return fmt.Errorf("发布资源路径不能包含工程源码或隐藏文件")
	}
	return nil
}
