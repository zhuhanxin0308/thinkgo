package command

import (
	"fmt"
	"path/filepath"

	"thinkgo/framework/console"
	modeldb "thinkgo/framework/db"
)

// MakeModel command
type MakeModel struct {
	console.Command
}

func (c *MakeModel) Configure() {
	c.Signature = "make:model"
	c.Description = "Create a new model class with snake_case table naming"
	c.AddArgument("name", "Model type name", true)
}

func (c *MakeModel) Execute(input *console.Input, output *console.Output) error {
	structName, err := normalizedGeneratorInput(c.App, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid model name: %w", err)
	}

	// 统一把命令参数转换成结构体名和默认文件名，避免新模型继续沿用旧复数化命名习惯。
	fileBaseName := modeldb.ToSnakeCase(structName)

	// Content
	content := fmt.Sprintf(`package model

import (
	"thinkgo/framework/db"
)

// %s 数据模型
// 默认表名规则：结构体名转为 snake_case，不自动复数化；表前缀由数据库配置统一追加。
type %s struct {
	*db.Model
}

// New%s 创建模型实例
func New%s(database *db.DB) (*%s, error) {
	m := &%s{}
	model, err := db.NewModelAuto(database, m)
	if err != nil {
		return nil, err
	}
	m.Model = model
	return m, nil
}
`, structName, structName, structName, structName, structName, structName)

	if err := writeGeneratedAppSource(c.App, filepath.Join("app", "model"), fileBaseName+".go", []byte(content)); err != nil {
		return fmt.Errorf("create model %s: %w", structName, err)
	}

	output.Success(fmt.Sprintf("Model %s created successfully.", structName))
	return nil
}
