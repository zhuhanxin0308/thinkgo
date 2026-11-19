package validate

import (
	"testing"
	"time"
)

// TestTimePointerValidationRules 验证 *time.Time、**time.Time 与 nil 时间指针在各类日期及标量规则下行为正确。
func TestTimePointerValidationRules(t *testing.T) {
	base := time.Date(2026, 6, 15, 10, 30, 0, 0, time.UTC)
	later := base.Add(2 * time.Hour)
	earlier := base.Add(-2 * time.Hour)
	basePtr := &base
	baseDoublePtr := &basePtr
	laterPtr := &later
	earlierPtr := &earlier
	var nilTime *time.Time

	v := NewValidator().SetRules(map[string]string{
		"published_at": "required|date|dateFormat:" + time.RFC3339Nano + "|after:2026-06-01|before:2026-07-01",
		"double_ptr":   "required|date|afterField:started_at|beforeField:ended_at",
		"started_at":   "required|date",
		"ended_at":     "required|date",
		"optional_at":  "date",
		"eq_time":      "eq:" + base.Format(time.RFC3339Nano),
	})

	result, err := v.Validate(map[string]interface{}{
		"published_at": basePtr,
		"double_ptr":   baseDoublePtr,
		"started_at":   earlierPtr,
		"ended_at":     laterPtr,
		"eq_time":      basePtr,
	})
	if err != nil {
		t.Fatalf("执行时间指针校验出错: %v", err)
	}
	if !result.Valid() {
		t.Fatalf("有效 *time.Time 与 **time.Time 字段应通过校验，实际错误=%#v", result.Errors())
	}

	// nil *time.Time 在 required 或显式传入 date 字段时必须失败，时间顺序倒挂的 *time.Time 在 afterField/beforeField 下必须失败。
	invalidResult, err := v.Validate(map[string]interface{}{
		"published_at": nilTime,
		"double_ptr":   laterPtr,
		"started_at":   laterPtr,
		"ended_at":     earlierPtr,
		"optional_at":  nilTime,
		"eq_time":      earlierPtr,
	}, CollectAllErrors())
	if err != nil {
		t.Fatalf("执行非法时间指针校验出错: %v", err)
	}
	if invalidResult.Valid() {
		t.Fatal("nil *time.Time 和违反日期约束的 *time.Time 必须被拒绝")
	}
	failedFields := make(map[string]bool)
	for _, violation := range invalidResult.Violations() {
		failedFields[violation.Field] = true
	}
	for _, field := range []string{"published_at", "double_ptr", "optional_at", "eq_time"} {
		if !failedFields[field] {
			t.Fatalf("字段 %q 应报告校验失败，实际违规=%#v", field, invalidResult.Violations())
		}
	}

	// 覆盖 requireIf 关联字段与 in/notIn/regex 标量规则中的 *time.Time。
	relatedValidator := NewValidator().SetRules(map[string]string{
		"note":         "requireIf:published_at," + base.Format(time.RFC3339Nano),
		"published_at": "in:" + base.Format(time.RFC3339Nano) + "|notIn:" + earlier.Format(time.RFC3339Nano) + `|regex:^2026-06-15T`,
	})
	if res, err := relatedValidator.Validate(map[string]interface{}{
		"published_at": basePtr,
		"note":         "scheduled",
	}); err != nil || !res.Valid() {
		t.Fatalf("requireIf/in/notIn/regex 应支持 *time.Time: err=%v errors=%#v", err, res.Errors())
	}
	if res, err := relatedValidator.Validate(map[string]interface{}{
		"published_at": basePtr,
		"note":         "",
	}); err != nil || res.Valid() {
		t.Fatalf("当 *time.Time 命中 requireIf 条件时，空 note 必须被拒绝: err=%v", err)
	}
}
