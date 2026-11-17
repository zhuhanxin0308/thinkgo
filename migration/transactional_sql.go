package migration

import (
	"context"
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/sqllex"
)

type migrationSQLSteps struct {
	up, down []string
}

func implicitCommitDialect(dialect string) bool {
	return dialect == "mysql" || dialect == "oracle"
}

// migrationTransactionMode 分别判断 Up 和 Down。程序化声明启用受限执行器，防止误用会隐式提交的 SQL。
func migrationTransactionMode(current Migration, operation JournalOperation, dialect string) (atomic, guarded bool) {
	if !implicitCommitDialect(dialect) {
		return true, false
	}
	migration, ok := current.(*definition)
	if !ok {
		return false, false
	}
	if migration.transactional {
		return true, true
	}
	if migration.sqlSteps == nil {
		return false, false
	}
	statements := migration.sqlSteps.up
	if operation == JournalOperationRevert {
		statements = migration.sqlSteps.down
	}
	for _, statement := range statements {
		if !transactionalDML(dialect, statement) {
			return false, false
		}
	}
	return len(statements) != 0, false
}

func transactionalDML(dialect, statement string) bool {
	switch sqllex.MigrationStatementCommand(statement, dialect) {
	case "INSERT", "UPDATE", "DELETE":
		return true
	case "REPLACE":
		return dialect == "mysql"
	case "MERGE":
		return dialect == "oracle"
	}
	return false
}

type transactionalMigrationExecutor struct {
	executor Executor
	dialect  string
}

func (executor transactionalMigrationExecutor) ExecuteContext(ctx context.Context, statement string, args ...any) (int64, error) {
	if !transactionalDML(executor.dialect, statement) {
		return 0, fmt.Errorf("%w: 事务型迁移仅允许执行可识别的单条 DML", ErrInvalidMigration)
	}
	return executor.executor.ExecuteContext(ctx, statement, args...)
}

func (executor transactionalMigrationExecutor) QueryContext(ctx context.Context, statement string, args ...any) ([]map[string]any, error) {
	if sqllex.MigrationStatementCommand(statement, executor.dialect) != "SELECT" {
		return nil, fmt.Errorf("%w: 事务型迁移仅允许单条 SELECT 查询", ErrInvalidMigration)
	}
	return executor.executor.QueryContext(ctx, statement, args...)
}
