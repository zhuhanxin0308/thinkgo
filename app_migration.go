package framework

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/framework/migration"
)

// RegisterMigration 在当前应用实例中注册数据库迁移。
func (app *App) RegisterMigration(current migration.Migration) error {
	if app == nil {
		return ErrNilApplication
	}
	app.registrationMu.Lock()
	defer app.registrationMu.Unlock()
	if err := app.ensureRegistrationOpen(); err != nil {
		return err
	}
	if app.migrations == nil {
		app.migrations = migration.NewRegistry()
		if err := app.Instance(serviceKeyMigration, app.migrations); err != nil {
			return fmt.Errorf("绑定迁移服务失败: %w", err)
		}
	}
	return app.migrations.Register(current)
}
