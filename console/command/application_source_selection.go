package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const applicationSourceSelectionTimeout = 2 * time.Minute

// selectedApplicationFiles 复用 Go 工具链实际选择的源码，统一自定义标签、目标平台、CGO 和平台特性标签。
// 每次发现事务只枚举一次，不修改进程环境；已有生成文件仍由语义解析层排除。
func (semantic *applicationSemanticContext) selectedApplicationFiles(directory, packageName string) (map[string]bool, error) {
	semantic.sourceFilesOnce.Do(func() {
		semantic.sourcePackages, semantic.sourceSizes, semantic.sourceFilesErr = loadApplicationSourcePackages(semantic.basePath)
	})
	if semantic.sourceFilesErr != nil {
		return nil, semantic.sourceFilesErr
	}
	key := filepath.Clean(directory)
	pkg, exists := semantic.sourcePackages[key]
	if !exists {
		// ./... 会跳过下划线目录；业务发现已显式选中的合法目录必须由 Go 单独检查。
		ctx, cancel := context.WithTimeout(context.Background(), applicationSourceSelectionTimeout)
		defer cancel()
		output, err := listBuildSourcePackages(ctx, semantic.basePath, "./"+filepath.ToSlash(key))
		if err != nil {
			return nil, err
		}
		packages, err := decodeApplicationSourcePackages(semantic.basePath, output)
		if err != nil {
			return nil, err
		}
		pkg = packages[key]
		semantic.sourcePackages[key] = pkg
	}
	if pkg.Error != nil && !(len(pkg.GoFiles) == 0 && len(pkg.CgoFiles) == 0 && len(pkg.IgnoredGoFiles) > 0) {
		if packageName != "" && pkg.Name != packageName {
			return nil, fmt.Errorf("应用目录 %q 的包名必须为 %s: %s", directory, packageName, pkg.Error.Err)
		}
		return nil, fmt.Errorf("检查应用目录 %q 的构建约束失败: %s", directory, pkg.Error.Err)
	}
	selected := make(map[string]bool, len(pkg.GoFiles)+len(pkg.CgoFiles))
	for _, name := range append(append([]string(nil), pkg.GoFiles...), pkg.CgoFiles...) {
		selected[name] = true
	}
	return selected, nil
}

func loadApplicationSourcePackages(basePath string) (map[string]commentPackage, types.Sizes, error) {
	ctx, cancel := context.WithTimeout(context.Background(), applicationSourceSelectionTimeout)
	defer cancel()
	output, err := listOpenAPICommentPackages(ctx, basePath)
	if err != nil {
		return nil, nil, err
	}
	packages, err := decodeApplicationSourcePackages(basePath, output)
	if err != nil {
		return nil, nil, err
	}
	// go/types 的整数宽度必须与源码和外部包导出数据使用同一目标，避免 32 位目标沿用宿主大小。
	process := exec.CommandContext(ctx, "go", "env", "GOARCH")
	process.Dir = basePath
	process.Env = append(os.Environ(), "GOWORK=off")
	architecture, err := process.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("读取应用编译目标失败: %w", err)
	}
	sizes := types.SizesFor("gc", strings.TrimSpace(string(architecture)))
	if sizes == nil {
		return nil, nil, fmt.Errorf("应用编译目标不支持类型大小检查: %q", strings.TrimSpace(string(architecture)))
	}
	return packages, sizes, nil
}

func decodeApplicationSourcePackages(basePath string, output []byte) (map[string]commentPackage, error) {
	base, err := filepath.Abs(basePath)
	if err != nil {
		return nil, err
	}
	packages := make(map[string]commentPackage)
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var pkg commentPackage
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("解析应用构建源码清单失败: %w", err)
		}
		directory, err := filepath.Rel(base, pkg.Dir)
		if err != nil || !filepath.IsLocal(directory) {
			return nil, fmt.Errorf("应用源码目录 %q 不属于当前模块", pkg.Dir)
		}
		packages[filepath.Clean(directory)] = pkg
	}
	return packages, nil
}
