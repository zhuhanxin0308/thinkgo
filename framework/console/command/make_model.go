package command

import (
	"fmt"
	"os"
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
}

func (c *MakeModel) Execute(input *console.Input, output *console.Output) {
	structName, err := normalizeGeneratorName(input.GetArgument(0), "")
	if err != nil {
		output.Error("Invalid model name: " + err.Error())
		return
	}

	// 统一把命令参数转换成结构体名和默认文件名，避免新模型继续沿用旧复数化命名习惯。
	fileBaseName := modeldb.ToSnakeCase(structName)

	// File path
	filename := filepath.Join(c.App.BasePath, "app", "model", fileBaseName+".go")

	// Check if exists
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		output.Error(fmt.Sprintf("Model %s already exists.", structName))
		return
	}

	// Ensure directory exists
	dir := filepath.Join(c.App.BasePath, "app", "model")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			output.Error(fmt.Sprintf("Failed to create model directory: %v", err))
			return
		}
	}

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
func New%s(database *db.DB) *%s {
	m := &%s{}
	m.Model = db.NewModelAuto(database, m)
	return m
}
`, structName, structName, structName, structName, structName, structName)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create model: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Model %s created successfully.", structName))
}
