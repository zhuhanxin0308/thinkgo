package command

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/framework/console"
	modeldb "github.com/zhuhanxin0308/thinkgo/framework/db"
)

// MakeModel 生成可由应用容器自动解析的数据模型。
type MakeModel struct {
	console.Command
}

func (c *MakeModel) Configure() {
	c.Signature = "make:model"
	c.Description = "Create a new model class with snake_case table naming"
	c.AddArgument("name", "Model type name", true)
	addApplicationSelectionOption(&c.Command)
}

func (c *MakeModel) Execute(input *console.Input, output *console.Output) error {
	target, err := normalizedGeneratorTarget(&c.Command, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid model name: %w", err)
	}
	structName := target.name

	// 统一把命令参数转换成结构体名和默认文件名，避免新模型继续沿用旧复数化命名习惯。
	fileBaseName := modeldb.ToSnakeCase(structName)

	// 模型只声明结构体即可参与自动装配，构造函数仅作为显式传入数据库时的便捷入口。
	content := fmt.Sprintf(`package model

import (
	"context"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

// %s 数据模型
// 默认表名规则：结构体名转为 snake_case，不自动复数化；表前缀由数据库配置统一追加。
type %s struct {
	*db.Model
}

// New%s 使用指定数据库创建模型；应用代码也可通过框架的 ResolveModel 自动解析。
func New%s(database *db.DB) (*%s, error) {
	m := &%s{}
	_, err := db.NewModelFor(context.Background(), database, m)
	if err != nil {
		return nil, err
	}
	return m, nil
}
`, structName, structName, structName, structName, structName, structName)

	if err := writeAndRefreshGeneratedApplicationSource(c.App, target, "model", fileBaseName+".go", []byte(content)); err != nil {
		return fmt.Errorf("create model %s: %w", structName, err)
	}

	output.Success(fmt.Sprintf("Model %s created successfully.", structName))
	return nil
}
