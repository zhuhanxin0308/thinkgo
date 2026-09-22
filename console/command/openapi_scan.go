package command

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	"github.com/zhuhanxin0308/thinkgo/v3/openapi"
)

type commentPackage struct {
	Dir            string
	Name           string
	ImportPath     string
	GoFiles        []string
	CgoFiles       []string
	IgnoredGoFiles []string
	Error          *struct{ Err string }
}

// scanOpenAPIComments 通过 go list 选择真实构建文件，尊重 GOOS、GOARCH、CGO 与 GOFLAGS 构建标签。
// 不执行应用初始化、数据库连接或用户处理器；嵌套模块和依赖源码不属于扫描范围。
func scanOpenAPIComments(ctx context.Context, basePath string) (openapi.SourceComments, error) {
	if err := ctx.Err(); err != nil {
		return openapi.SourceComments{}, err
	}
	module, err := readApplicationModulePath(basePath)
	if err != nil {
		return openapi.SourceComments{}, err
	}
	source := openapi.SourceComments{Version: openapi.SourceCommentsVersion, Module: module, Files: make(map[string]string), Handlers: make(map[string]openapi.HandlerComments), Types: make(binding.Documentation)}
	output, err := listOpenAPICommentPackages(ctx, basePath)
	if err != nil {
		return source, err
	}
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return source, err
	}
	defer root.Close()
	base, err := filepath.Abs(basePath)
	if err != nil {
		return source, err
	}
	fileSet := token.NewFileSet()
	var files []commentSourceFile
	packages := make(map[string]string)
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		if err := ctx.Err(); err != nil {
			return source, err
		}
		var pkg commentPackage
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return source, err
		}
		if pkg.ImportPath != module && !strings.HasPrefix(pkg.ImportPath, module+"/") {
			continue
		}
		directory, err := filepath.Rel(base, pkg.Dir)
		if err != nil || !filepath.IsLocal(directory) {
			return source, fmt.Errorf("API 源码目录 %q 不属于当前模块", pkg.Dir)
		}
		if filepath.ToSlash(directory) == openAPICommentsDirectory {
			continue
		}
		if pkg.Error != nil {
			return source, fmt.Errorf("API 源码包 %s 无效: %s", pkg.ImportPath, pkg.Error.Err)
		}
		packages[pkg.ImportPath] = pkg.Name
		for _, name := range append(pkg.GoFiles, pkg.CgoFiles...) {
			if name == applicationDiscoveryFilename || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if filepath.Base(name) != name {
				return source, fmt.Errorf("API 源码文件名 %q 非法", name)
			}
			path := filepath.Join(directory, name)
			content, err := root.ReadFile(path)
			if err != nil {
				return source, err
			}
			file, err := parser.ParseFile(fileSet, filepath.ToSlash(path), content, parser.ParseComments|parser.SkipObjectResolution)
			if err != nil {
				return source, err
			}
			if err := collectOpenAPIComments(&source, fileSet, file, pkg.ImportPath); err != nil {
				return source, err
			}
			files = append(files, commentSourceFile{packagePath: pkg.ImportPath, file: file})
			digest := sha256.Sum256(content)
			source.Files[filepath.ToSlash(path)] = hex.EncodeToString(digest[:])
		}
	}
	return source, inheritAPITypeComments(source.Types, files, packages, fileSet)
}
