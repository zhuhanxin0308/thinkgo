package framework

import (
	"fmt"
	"path/filepath"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

// DatabaseSchemaIdentity 返回与应用连接配置绑定的结构缓存身份。
func (app *App) DatabaseSchemaIdentity(connection string) (string, error) {
	if app == nil || app.config == nil {
		return "", ErrNilApplication
	}
	if err := db.ValidateConnectionName(connection); err != nil {
		return "", err
	}
	configuration := app.config.GetMap("database")
	connections, ok := configuration["connections"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("数据库连接配置不存在")
	}
	raw, ok := connections[connection].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("数据库连接 %q 未配置", connection)
	}
	parsed, err := readDatabaseConfig(raw)
	if err != nil {
		return "", err
	}
	if err := applyThinkPHPDatabaseDefaults(configuration, &parsed); err != nil {
		return "", err
	}
	applyDatabaseFallbacks(&parsed)
	return db.SchemaCacheIdentity(parsed), nil
}

func (app *App) loadDatabaseSchemaCache(spec databaseConnectionSpec, database *db.DB) {
	if !spec.config.FieldsCache {
		return
	}
	path := filepath.Join(app.GetRuntimePath(), "schema", spec.name+".json")
	content, exists, err := readApplicationOptimizationFile(app.GetRootPath(), path, db.MaxSchemaCacheBytes)
	if err == nil && exists {
		err = database.LoadSchemaCache(content, db.SchemaCacheIdentity(spec.config))
	}
	if err != nil && app.log != nil {
		// 缓存损坏时回退数据库原有查询能力，也允许 optimize:schema 重新生成缓存。
		app.log.Warning(fmt.Sprintf("连接 %q 结构缓存不可用: %v", spec.name, err))
	}
}
