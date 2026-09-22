package command

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// MakeCRUD 从现有模型和显式字段白名单生成可运行的业务 API。
type MakeCRUD struct{ console.Command }

// Configure 声明字段、主键与资源路径，禁止自动公开模型的全部字段。
func (c *MakeCRUD) Configure() {
	c.Signature = "make:crud"
	c.Description = "Generate typed CRUD API and contract tests from an existing model"
	c.AddArgument("name", "Existing model type name", true)
	addApplicationSelectionOption(&c.Command)
	c.AddOption("write", "", "Comma-separated writable Go fields", "")
	c.AddOption("read", "", "Comma-separated output Go fields", "")
	c.AddOption("key", "", "Model primary-key Go field", crudDefaultKey)
	c.AddOption("path", "", "Explicit resource path, for example /users", "")
}

// Execute 先完成语义检查与全部源码生成，再独占写入并刷新模型发现清单。
func (c *MakeCRUD) Execute(input *console.Input, output *console.Output) error {
	target, err := normalizedGeneratorTarget(&c.Command, input, output, "")
	if err != nil {
		return err
	}
	if err := input.Context().Err(); err != nil {
		return err
	}
	plan, err := buildCRUDPlan(c.App.BasePath, target, input)
	if err != nil {
		return err
	}
	source, err := renderCRUDSource(plan)
	if err != nil {
		return err
	}
	testSource, err := renderCRUDTestSource(plan)
	if err != nil {
		return err
	}
	if err := validateCRUDSource(plan, source, testSource); err != nil {
		return err
	}
	created := make([]string, 0, 2)
	rollback := func(cause error) error {
		for index := len(created) - 1; index >= 0; index-- {
			cause = errors.Join(cause, removeGeneratedAppSource(c.App, filepath.Join("app", target.application, "api"), created[index]))
		}
		return cause
	}
	for _, file := range []struct {
		name    string
		content []byte
	}{{plan.FileName + ".go", source}, {plan.FileName + "_test.go", testSource}} {
		if err := input.Context().Err(); err != nil {
			return rollback(err)
		}
		if err := writeGeneratedAppSource(c.App, target, "api", file.name, file.content); err != nil {
			return rollback(err)
		}
		created = append(created, file.name)
	}
	if err := RefreshControllerDiscovery(c.App); err != nil {
		return rollback(err)
	}
	output.Success(fmt.Sprintf("CRUD API %s created. Register it with api.Register%sRoutes(router, registry).", target.name, target.name))
	return nil
}
