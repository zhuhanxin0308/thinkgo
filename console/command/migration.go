package command

import (
	"fmt"
	"strconv"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/migration"
)

type migrationStoreFactory func(*db.DB) (migration.Store, error)

// Migrate 应用当前应用全部待执行迁移。
type Migrate struct {
	console.Command
	storeFactory migrationStoreFactory
}

func (command *Migrate) Configure() {
	command.Signature = "migrate"
	command.Description = "Apply pending database migrations"
	addApplicationSelectionOption(&command.Command)
}

func (command *Migrate) Execute(input *console.Input, output *console.Output) error {
	if input == nil {
		return console.ErrInvalidInput
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	applications, err := compiledApplicationsForCommand(command.App, input.GetOption("app"), false)
	if err != nil {
		return err
	}
	runner, err := buildMigrationRunner(applications[0].application, command.storeFactory)
	if err != nil {
		return err
	}
	result, err := runner.Apply(input.Context())
	if err != nil {
		return err
	}
	if len(result.Names) == 0 {
		output.Writeln("No pending migrations.")
		return nil
	}
	for _, name := range result.Names {
		output.Writeln(fmt.Sprintf("Migrated  %s  [batch %d]", name, result.Batch))
	}
	return nil
}

// MigrateRollback 按批次回滚当前应用最近应用的迁移。
type MigrateRollback struct {
	console.Command
	storeFactory migrationStoreFactory
}

func (command *MigrateRollback) Configure() {
	command.Signature = "migrate:rollback"
	command.Description = "Rollback recent database migration batches"
	addApplicationSelectionOption(&command.Command)
	command.AddOption("batches", "b", "Number of recent batches to rollback", "1")
}

func (command *MigrateRollback) Execute(input *console.Input, output *console.Output) error {
	if input == nil {
		return console.ErrInvalidInput
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	batches, err := strconv.Atoi(input.GetOption("batches"))
	if err != nil || batches <= 0 || batches > migration.MaximumRollbackBatches {
		return fmt.Errorf("回滚批次数必须是 1 到 %d 之间的整数", migration.MaximumRollbackBatches)
	}
	applications, err := compiledApplicationsForCommand(command.App, input.GetOption("app"), false)
	if err != nil {
		return err
	}
	runner, err := buildMigrationRunner(applications[0].application, command.storeFactory)
	if err != nil {
		return err
	}
	result, err := runner.Rollback(input.Context(), batches)
	if err != nil {
		return err
	}
	if len(result.Names) == 0 {
		output.Writeln("Nothing to rollback.")
		return nil
	}
	for _, name := range result.Names {
		output.Writeln(fmt.Sprintf("Rolled back  %s", name))
	}
	return nil
}

// MigrateStatus 输出当前代码迁移与数据库历史的校验状态。
type MigrateStatus struct {
	console.Command
	storeFactory migrationStoreFactory
}

func (command *MigrateStatus) Configure() {
	command.Signature = "migrate:status"
	command.Description = "Show database migration status"
	addApplicationSelectionOption(&command.Command)
}

func (command *MigrateStatus) Execute(input *console.Input, output *console.Output) error {
	if input == nil {
		return console.ErrInvalidInput
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	applications, err := compiledApplicationsForCommand(command.App, input.GetOption("app"), false)
	if err != nil {
		return err
	}
	runner, err := buildMigrationRunner(applications[0].application, command.storeFactory)
	if err != nil {
		return err
	}
	statuses, err := runner.Status(input.Context())
	if err != nil {
		return err
	}
	if len(statuses) == 0 {
		output.Writeln("No migrations registered.")
		return nil
	}
	output.Writeln(fmt.Sprintf("%-8s %-8s %s", "Status", "Batch", "Migration"))
	for _, status := range statuses {
		state := "Pending"
		batch := "-"
		if status.Applied {
			state = "Applied"
			batch = strconv.FormatInt(status.Batch, 10)
		}
		output.Writeln(fmt.Sprintf("%-8s %-8s %s", state, batch, status.Name))
	}
	return nil
}

func buildMigrationRunner(app *framework.App, factory migrationStoreFactory) (*migration.Runner, error) {
	if app == nil {
		return nil, framework.ErrNilApplication
	}
	registry, err := framework.ResolveServiceAs[*migration.Registry](app, framework.ServiceMigration)
	if err != nil {
		return nil, fmt.Errorf("迁移注册表不可用: %w", err)
	}
	database, err := framework.ResolveServiceAs[*db.DB](app, framework.ServiceDB)
	if err != nil {
		return nil, fmt.Errorf("迁移数据库不可用: %w", err)
	}
	if factory == nil {
		factory = func(database *db.DB) (migration.Store, error) {
			return migration.NewDatabaseStore(database)
		}
	}
	store, err := factory(database)
	if err != nil {
		return nil, fmt.Errorf("创建迁移存储失败: %w", err)
	}
	runner, err := migration.NewRunner(registry, store)
	if err != nil {
		return nil, fmt.Errorf("创建迁移运行器失败: %w", err)
	}
	return runner, nil
}
