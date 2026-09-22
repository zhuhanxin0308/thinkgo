package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const openAPIPrivateFileMode = 0o600

// listOpenAPICommentPackages 使用临时模块清单枚举源码，首次接入时也不会改写用户的依赖文件。
// -find 只检查目标包的构建文件，不加载尚未生成的 apidoc 包或执行应用代码。
func listOpenAPICommentPackages(ctx context.Context, basePath string) (_ []byte, returnErr error) {
	return listBuildSourcePackages(ctx, basePath, "./...")
}

// listBuildSourcePackages 为发现和文档生成提供同一构建文件选择器，显式目录用于补充通配规则忽略的合法应用。
func listBuildSourcePackages(ctx context.Context, basePath string, patterns ...string) (_ []byte, returnErr error) {
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	module, err := root.ReadFile("go.mod")
	if err != nil {
		return nil, err
	}
	sum, err := root.ReadFile("go.sum")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	temporary, err := os.MkdirTemp("", "thinkgo-openapi-mod-")
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, os.RemoveAll(temporary)) }()
	staging, err := os.OpenRoot(temporary)
	if err != nil {
		return nil, err
	}
	defer staging.Close()
	moduleFile := filepath.Join(temporary, "source.mod")
	if err := staging.WriteFile("source.mod", module, openAPIPrivateFileMode); err != nil {
		return nil, err
	}
	if sum != nil {
		if err := staging.WriteFile("source.sum", sum, openAPIPrivateFileMode); err != nil {
			return nil, err
		}
	}
	// #nosec G204 -- 命令与参数固定，模块路径仅来自本函数创建的临时目录，未拼接 shell。
	process := exec.CommandContext(ctx, "go", "list", "-mod=mod", "-modfile="+moduleFile, "-e", "-find", "-json", "--")
	process.Args = append(process.Args, patterns...)
	process.Dir = basePath
	process.Env = append(os.Environ(), "GOWORK=off")
	var stderr bytes.Buffer
	process.Stderr = &stderr
	output, err := process.Output()
	if err != nil {
		return nil, errors.Join(ctx.Err(), fmt.Errorf("枚举 API 源码失败: %w: %s", err, stderr.String()))
	}
	return output, nil
}
