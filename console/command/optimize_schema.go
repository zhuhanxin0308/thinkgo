package command

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

// OptimizeSchema 将真实数据库字段结构缓存到各应用运行目录。
type OptimizeSchema struct{ console.Command }

// Configure 保持 ThinkPHP 的应用、连接和数据表选项。
func (command *OptimizeSchema) Configure() {
	command.Signature = "optimize:schema"
	command.Description = "Build database schema cache."
	command.AddArgument("dir", "dir name .", false)
	command.AddOption("connection", "", "connection name .", "")
	command.AddOption("table", "", "table name, or * for all tables .", "")
}

// Execute 支持显式表、全部表及模型配置钩子定义的真实表名。
func (command *OptimizeSchema) Execute(input *console.Input, output *console.Output) error {
	if command == nil || command.App == nil {
		return framework.ErrNilApplication
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	if input == nil {
		input = console.NewInput()
	}
	directory, err := optimizeDirectory(input)
	if err != nil {
		return err
	}
	applications, err := compiledApplicationsForOptimization(command.App, directory, "model")
	if err != nil {
		return err
	}
	for _, application := range applications {
		if err := optimizeApplicationSchema(input, command.App, application); err != nil {
			return fmt.Errorf("优化应用 %q 数据表结构失败: %w", application.CurrentApplicationName(), err)
		}
	}
	output.Info("Succeed!")
	return output.Err()
}

func optimizeApplicationSchema(input *console.Input, project, application *framework.App) error {
	if err := input.Context().Err(); err != nil {
		return err
	}
	table := strings.TrimSpace(input.GetOption("table"))
	types := application.RegisteredModelTypes()
	if table == "" && len(types) == 0 {
		return nil
	}
	connection := strings.TrimSpace(input.GetOption("connection"))
	if connection == "" {
		connection = application.Config().GetString("database.default")
	}
	identity, err := application.DatabaseSchemaIdentity(connection)
	if err != nil {
		return err
	}
	manager := application.DBManager()
	if manager == nil {
		return db.ErrDatabaseUnavailable
	}
	database, err := manager.Connection(connection)
	if err != nil {
		return err
	}
	tables := []string{table}
	switch table {
	case "*":
		tables, err = database.SchemaTables(input.Context(), "")
	case "":
		tables = nil
		for _, modelType := range types {
			for modelType.Kind() == reflect.Ptr {
				modelType = modelType.Elem()
			}
			model, modelErr := db.NewModelFor(input.Context(), database, reflect.New(modelType).Interface())
			if modelErr != nil {
				return modelErr
			}
			name, modelErr := model.SchemaTable()
			if modelErr != nil {
				return modelErr
			}
			tables = append(tables, name)
		}
	}
	if err != nil {
		return err
	}
	sort.Strings(tables)
	content, err := database.ExportSchemaCache(input.Context(), identity, tables)
	if err != nil {
		return err
	}
	return writeProjectFileAtomically(project, filepath.Join(application.GetRuntimePath(), "schema", connection+".json"), content)
}
