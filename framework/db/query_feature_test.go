package db

import (
	"strings"
	"testing"
	"time"
)

func TestWhereSupportsStructuredInputs(t *testing.T) {
	database := NewDB(&mockConnection{})

	q := database.Table("users").Where(map[string]interface{}{
		"status": 1,
		"type":   "admin",
	})
	if q.err != nil {
		t.Fatalf("结构化 map 条件不应报错，实际为 %v", q.err)
	}
	if len(q.where) != 1 {
		t.Fatalf("map 条件应编译成单个 where 片段，实际为 %d", len(q.where))
	}
	if !strings.Contains(q.where[0], "status = ?") || !strings.Contains(q.where[0], "type = ?") {
		t.Fatalf("map 条件编译结果不正确，实际为 %q", q.where[0])
	}

	q2 := database.Table("users").Where([][]interface{}{
		{"age", ">=", 18},
		{"status", "=", 1},
	})
	if q2.err != nil {
		t.Fatalf("三元组条件不应报错，实际为 %v", q2.err)
	}
	if len(q2.where) != 1 || !strings.Contains(q2.where[0], "age >= ?") || !strings.Contains(q2.where[0], "status = ?") {
		t.Fatalf("三元组条件编译结果不正确，实际为 %#v", q2.where)
	}
}

func TestWhereSupportsClosureOrColumnTimeAndExpression(t *testing.T) {
	database := NewDB(&mockConnection{})

	q := database.Table("users").
		Where(func(group *ConditionGroup) {
			group.Where("status", 1).WhereOr("role", "admin")
		}).
		WhereOr("type", "staff").
		WhereColumn("start_time", "<", "end_time").
		WhereExp("updated_at", ">", "NOW()")

	if q.err != nil {
		t.Fatalf("增强 Where 链式调用不应报错，实际为 %v", q.err)
	}
	if len(q.where) != 3 {
		t.Fatalf("增强 Where 应生成 3 个 where 片段，实际为 %d", len(q.where))
	}
	if q.where[0] != "(status = ? OR role = ? OR type = ?)" {
		t.Fatalf("闭包 + WhereOr 编译结果不正确，实际为 %q", q.where[0])
	}
	if q.where[1] != "start_time < end_time" {
		t.Fatalf("WhereColumn 编译结果不正确，实际为 %q", q.where[1])
	}
	if q.where[2] != "updated_at > NOW()" {
		t.Fatalf("WhereExp 编译结果不正确，实际为 %q", q.where[2])
	}

	timeQuery := database.Table("users").WhereTime("create_time", "today")
	if timeQuery.err != nil {
		t.Fatalf("WhereTime(today) 不应报错，实际为 %v", timeQuery.err)
	}
	if len(timeQuery.where) != 1 || timeQuery.where[0] != "create_time BETWEEN ? AND ?" {
		t.Fatalf("WhereTime(today) 编译结果不正确，实际为 %#v", timeQuery.where)
	}
	if len(timeQuery.args) != 2 {
		t.Fatalf("WhereTime(today) 应生成 2 个时间参数，实际为 %d", len(timeQuery.args))
	}

	start, err := time.Parse(DefaultTimeFormat, timeQuery.args[0].(string))
	if err != nil {
		t.Fatalf("WhereTime 起始时间格式不正确，错误为 %v", err)
	}
	end, err := time.Parse(DefaultTimeFormat, timeQuery.args[1].(string))
	if err != nil {
		t.Fatalf("WhereTime 结束时间格式不正确，错误为 %v", err)
	}
	if !start.Before(end) {
		t.Fatalf("WhereTime 时间范围应为开始早于结束，实际 start=%s end=%s", start, end)
	}
}
