package db

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime/debug"
	"testing"
	"time"
)

// TestDatabaseSnapshotRejectsCycles 在独立受限进程中验证拒绝环，旧实现不会拖垮主测试进程。
func TestDatabaseSnapshotRejectsCycles(t *testing.T) {
	const childFlag = "THINKGO_SNAPSHOT_CYCLE_CHILD"
	if os.Getenv(childFlag) == "" {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDatabaseSnapshotRejectsCycles$")
		command.Env = append(os.Environ(), childFlag+"=1")
		output, err := command.CombinedOutput()
		if err != nil {
			const outputLimit = 1200
			if len(output) > outputLimit {
				output = output[:outputLimit]
			}
			t.Fatalf("循环输入未安全返回: %v\n%s", err, output)
		}
		return
	}
	debug.SetMaxStack(1 << 20)
	selfMap := map[string]interface{}{}
	selfMap["self"] = selfMap
	var pointer interface{}
	pointer = &pointer
	selfSlice := make([]interface{}, 1)
	selfSlice[0] = selfSlice
	for _, input := range []interface{}{selfMap, pointer, selfSlice} {
		connection := &modelBusinessConnection{}
		database := NewDB(connection)
		model := NewModel(database, "items")
		operations := []func() error{
			func() error { _, err := model.Insert(map[string]interface{}{"value": input}); return err },
			func() error {
				_, err := database.Table("items").Insert(map[string]interface{}{"value": input})
				return err
			},
			func() error { _, err := database.Table("items").Where("value", input).Find(); return err },
			func() error { _, err := database.Table("items").WhereRaw("value = ?", input).Find(); return err },
			func() error {
				_, err := database.Table("items").Where("id", 1).Update(map[string]interface{}{"value": input})
				return err
			},
			func() error {
				_, err := model.newModelQuery().InsertAll([]map[string]interface{}{{"value": input}})
				return err
			},
		}
		for index, operation := range operations {
			if err := operation(); !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("入口 %d 对 %T 未返回 ErrInvalidQuery: %v", index, input, err)
			}
			if connection.lastContext != nil {
				t.Fatal("非法值不应到达数据库驱动")
			}
		}
		_ = database.Close()
	}
}
