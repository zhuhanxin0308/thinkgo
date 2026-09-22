package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"os"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

const (
	OpenAPIGenerateSignature = "openapi:generate"
	openAPICommentsDirectory = "internal/apidoc"
	openAPICommentsGoPath    = openAPICommentsDirectory + "/comments_generated.go"
	openAPICommentsJSONPath  = openAPICommentsDirectory + "/comments_generated.json"
)

// OpenAPIGenerate 从当前构建目标的 Go 源码提取接口注释，生成可编译的独立元数据包。
type OpenAPIGenerate struct{ console.Command }

// Configure 提供生成与只读过期检查入口。
func (command *OpenAPIGenerate) Configure() {
	command.Signature = OpenAPIGenerateSignature
	command.Description = "Generate API documentation metadata from Go comments"
	command.AddBoolOption("check", "", "Check generated comments without writing files")
}

// Execute 在所有源码、指令与元数据校验通过后原子发布两个生成文件。
func (command *OpenAPIGenerate) Execute(input *console.Input, output *console.Output) error {
	if command == nil || command.App == nil {
		return framework.ErrNilApplication
	}
	if input == nil {
		return console.ErrInvalidInput
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	controllerDiscoveryTransactionMu.Lock()
	defer controllerDiscoveryTransactionMu.Unlock()
	sources, err := openAPICommentSources(input.Context(), command.App.BasePath)
	if err != nil {
		return err
	}
	if input.GetOption("check") == "true" {
		if err := checkOpenAPICommentSources(command.App.BasePath, sources); err != nil {
			return err
		}
		output.Success("OpenAPI comments are up to date.")
		return output.Err()
	}
	if err := input.Context().Err(); err != nil {
		return err
	}
	if err := verifyOpenAPICommentOwnership(command.App.BasePath); err != nil {
		return err
	}
	if err := replaceGeneratedSourceBatch(command.App.BasePath, sources); err != nil {
		return err
	}
	output.Success("OpenAPI comments generated. Create the registry with apidoc.NewRegistry(info).")
	return output.Err()
}

func openAPICommentSources(ctx context.Context, basePath string) ([]generatedApplicationSource, error) {
	comments, err := scanOpenAPIComments(ctx, basePath)
	if err != nil {
		return nil, err
	}
	if err := comments.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(comments, "", "  ")
	if err != nil {
		return nil, err
	}
	source, err := format.Source([]byte(openAPICommentsTemplate))
	if err != nil {
		return nil, err
	}
	return []generatedApplicationSource{{relativePath: openAPICommentsJSONPath, source: append(encoded, '\n')}, {relativePath: openAPICommentsGoPath, source: source}}, nil
}

func checkOpenAPICommentSources(basePath string, sources []generatedApplicationSource) error {
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, source := range sources {
		current, err := root.ReadFile(source.relativePath)
		if err != nil || !bytes.Equal(current, source.source) {
			return fmt.Errorf("OpenAPI 注释产物 %q 缺失或已过期，请执行 openapi:generate", source.relativePath)
		}
	}
	return nil
}

// verifyOpenAPICommentOwnership 禁止首次生成覆盖同名用户包；已接管的产物可由生成器修复。
func verifyOpenAPICommentOwnership(basePath string) error {
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return err
	}
	defer root.Close()
	current, err := root.ReadFile(openAPICommentsGoPath)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := root.Stat(openAPICommentsJSONPath); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("不能覆盖未由 openapi:generate 管理的 %s", openAPICommentsJSONPath)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(current, []byte(openAPICommentsMarker)) {
		return fmt.Errorf("不能覆盖用户源码 %s", openAPICommentsGoPath)
	}
	return nil
}

// existingOpenAPICommentSources 让已启用的注释产物加入原有发现事务，未接入的项目不产生额外文件。
func existingOpenAPICommentSources(basePath string) ([]generatedApplicationSource, error) {
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if _, err := root.Stat(openAPICommentsGoPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := verifyOpenAPICommentOwnership(basePath); err != nil {
		return nil, err
	}
	return openAPICommentSources(context.Background(), basePath)
}

const openAPICommentsMarker = "// 此文件由 openapi:generate 生成，请勿手动修改。"

const openAPICommentsTemplate = openAPICommentsMarker + `

package apidoc

import (
	_ "embed"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/v3/openapi"
)

//go:generate go -C ../.. run ./cmd/think openapi:generate

// sourceJSON 随程序编译，不依赖生产环境保留 Go 源码。
//go:embed comments_generated.json
var sourceJSON []byte

// NewRegistry 为每个应用创建独立注册表，自动加载生成的接口和字段说明。
func NewRegistry(info openapi3.Info, options ...openapi.RegistryOption) (*openapi.Registry, error) {
	comments, err := openapi.ParseSourceComments(sourceJSON)
	if err != nil { return nil, err }
	configured := make([]openapi.RegistryOption, 0, len(options)+1)
	configured = append(configured, openapi.WithSourceComments(comments))
	configured = append(configured, options...)
	return openapi.NewRegistry(info, configured...)
}
`
