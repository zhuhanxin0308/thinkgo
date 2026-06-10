package db

import "testing"

// TestMongoBuildFilterRejectsUnparseable 验证无法解析的条件会返回错误，
// 而非静默丢弃导致空 filter 引发全表更新/删除。
func TestMongoBuildFilterRejectsUnparseable(t *testing.T) {
	c := &MongoConnection{}

	// 受支持的条件应正常解析。
	filter, err := c.buildFilter([]string{"age >= ?"}, []interface{}{18})
	if err != nil {
		t.Fatalf("受支持条件不应报错: %v", err)
	}
	if len(filter) != 1 {
		t.Fatalf("filter 应包含 1 个条件，实际 %d", len(filter))
	}

	// 无法解析的条件必须报错，避免退化为空 filter。
	if _, err := c.buildFilter([]string{"weird_unparseable_clause"}, nil); err == nil {
		t.Fatalf("无法解析的条件应返回错误，避免空 filter 全表操作")
	}

	// 参数数量不足也应报错而非吞掉条件。
	if _, err := c.buildFilter([]string{"age = ?"}, nil); err == nil {
		t.Fatalf("参数不足应返回错误")
	}
}
