package command

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/db/connector"
)

type schemaOptimizationModel struct{ *db.Model }

func (*schemaOptimizationModel) ConfigureModel(model *db.Model) error {
	model.Table("items")
	return nil
}

// TestOptimizeSchemaSQLiteConsumedOnRestart 验证模型配置、表前缀、全部表导出及下一进程真正使用缓存。
func TestOptimizeSchemaSQLiteConsumedOnRestart(t *testing.T) {
	connector.RegisterBuiltins()
	base := t.TempDir()
	ensureConsoleTestConfigFiles(t, base)
	configuration := db.Config{Type: "sqlite", Database: filepath.Join(base, "data.db"), Prefix: "tg_"}
	database, err := db.Connect(configuration)
	if err != nil {
		if strings.Contains(err.Error(), "CGO_ENABLED=0") || strings.Contains(err.Error(), "requires cgo") {
			t.Skipf("真实结构优化测试需要 CGO: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Execute("CREATE TABLE tg_items (id INTEGER PRIMARY KEY, title TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(map[string]interface{}{
		"default": "sqlite", "connections": map[string]interface{}{
			"sqlite": map[string]interface{}{"type": "sqlite", "database": configuration.Database, "prefix": "tg_", "fields_cache": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "config", "database.json"), content, 0644); err != nil {
		t.Fatal(err)
	}
	application := framework.NewApp(base)
	t.Cleanup(func() { _ = application.Close() })
	if err := application.RegisterModel("Item", &schemaOptimizationModel{}); err != nil {
		t.Fatal(err)
	}
	if err := application.Initialize(); err != nil {
		t.Fatal(err)
	}
	command := &OptimizeSchema{Command: console.Command{App: application}}
	command.Configure()
	for _, args := range [][]string{nil, {"--table", "*"}, {"--connection", "sqlite", "--table", "tg_items"}} {
		input := console.NewInput(args...)
		if err := input.Parse(command.GetArgumentDefinitions(), command.GetOptionDefinitions()); err != nil {
			t.Fatal(err)
		}
		if err := command.Execute(input, generatorTestOutput()); err != nil {
			t.Fatalf("优化 %v 失败: %v", args, err)
		}
	}
	cache, err := os.ReadFile(filepath.Join(base, "runtime", "schema", "sqlite.json"))
	if err != nil || !strings.Contains(string(cache), `"tg_items"`) || !strings.Contains(string(cache), `"title"`) {
		t.Fatalf("模型自定义表和字段没有进入缓存: %s %v", cache, err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	// 删除真实表后新应用仍可读到字段，证明读取的是落盘缓存而非再次查询系统表。
	if _, err := database.Execute("DROP TABLE tg_items"); err != nil {
		t.Fatal(err)
	}
	restarted := framework.NewApp(base)
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.Initialize(); err != nil {
		t.Fatal(err)
	}
	reopened, err := restarted.DBManager().Connection("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := reopened.GetSchemaInfo(context.Background(), "tg_items", false)
	if err != nil || len(schema.Columns) != 2 {
		t.Fatalf("新应用没有消费结构缓存: %+v %v", schema, err)
	}
	if _, err := reopened.GetSchemaInfo(context.Background(), "tg_items", true); err == nil {
		t.Fatal("强制刷新应报告真实表已不存在")
	}
}
