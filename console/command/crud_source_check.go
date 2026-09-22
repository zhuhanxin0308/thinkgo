package command

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/types"
	"strings"
)

// validateCRUDSource 检查生成实现与测试的全部函数体，避免方法遮蔽等问题在文件写入后才暴露。
// 复用模型解析上下文及导入缓存，始终以项目当前模块图的真实类型检查调用。
func validateCRUDSource(plan *crudPlan, source, testSource []byte) error {
	semantic := plan.semantic
	files := make([]*ast.File, 0, 2)
	for index, content := range [][]byte{source, testSource} {
		name := plan.FileName + ".go"
		if index != 0 {
			name = plan.FileName + "_test.go"
		}
		file, err := parser.ParseFile(semantic.fileSet, name, content, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		files = append(files, file)
	}
	configuration := types.Config{Importer: semantic}
	packagePath := strings.TrimSuffix(plan.ModelImport, "/model") + "/api"
	if _, err := configuration.Check(packagePath, semantic.fileSet, files, nil); err != nil {
		return fmt.Errorf("CRUD 源码与模型不兼容: %w", err)
	}
	return nil
}
