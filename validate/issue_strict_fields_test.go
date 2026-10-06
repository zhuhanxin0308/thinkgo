package validate

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestIssue37StrictFieldsAreOptInAndSceneScoped(t *testing.T) {
	v := NewValidator().SetRules(map[string]string{"name|姓名": "required", "admin": "boolean"}).SetScenes(map[string][]string{"edit": {"name"}})
	data := map[string]interface{}{"name": "user", "is_admin": true, "admin": true}
	legacy, err := v.Validate(data)
	if err != nil || !legacy.Valid() {
		t.Fatalf("legacy changed: %v %v", legacy, err)
	}
	strict, err := v.Validate(data, WithScene("edit"), DisallowUnknownFields(), CollectAllErrors())
	if err != nil {
		t.Fatal(err)
	}
	violations := strict.Violations()
	if len(violations) != 2 || violations[0].Field != "admin" || violations[1].Field != "is_admin" || violations[0].Rule != "unknown_field" {
		t.Fatalf("strict result: %+v", violations)
	}
	if len(data) != 3 {
		t.Fatal("validation mutated input")
	}
	good, err := v.Validate(map[string]interface{}{"name": "user"}, WithScene("edit"), DisallowUnknownFields())
	if err != nil || !good.Valid() {
		t.Fatalf("valid strict rejected: %v %v", good, err)
	}
	empty, err := NewValidator().Validate(map[string]interface{}{"extra": true}, DisallowUnknownFields())
	if err != nil || empty.Valid() {
		t.Fatal("empty allowlist accepted field")
	}
}
func TestIssue37StrictOptionDoesNotMutateSharedValidator(t *testing.T) {
	v := NewValidator().SetRules(map[string]string{"name": "required"})
	data := map[string]interface{}{"name": "ok", "extra": true}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(strict bool) {
			defer wg.Done()
			var options []Option
			if strict {
				options = []Option{DisallowUnknownFields()}
			}
			result, err := v.Validate(data, options...)
			if err != nil || result.Valid() == strict {
				t.Errorf("strict=%v result=%v err=%v", strict, result, err)
			}
		}(i%2 == 0)
	}
	wg.Wait()
}

func TestIssue37StrictFieldsReportDeterministically(t *testing.T) {
	v := NewValidator().SetRules(map[string]string{"name|姓名": "required"})
	data := map[string]interface{}{"z_extra": "private-user-value", "a_extra": true}
	for repeat := 0; repeat < 30; repeat++ {
		first, err := v.Validate(data, DisallowUnknownFields())
		if err != nil || len(first.Violations()) != 1 || first.Violations()[0].Field != "a_extra" {
			t.Fatalf("unstable first violation: %+v, %v", first.Violations(), err)
		}
		all, err := v.Validate(data, CollectAllErrors(), DisallowUnknownFields())
		violations := all.Violations()
		if err != nil || len(violations) != 3 {
			t.Fatalf("lost rule violations: %+v, %v", violations, err)
		}
		for index, name := range []string{"a_extra", "z_extra", "name"} {
			if violations[index].Field != name {
				t.Fatalf("unexpected order: %+v", violations)
			}
		}
		if violations[2].Rule != "required" || violations[2].Alias != "姓名" {
			t.Fatal("declared rule or alias changed")
		}
		if strings.Contains(strings.Join(all.Errors(), " "), "private-user-value") {
			t.Fatal("unknown field error contains its submitted value")
		}
	}
}

func TestIssue37StrictFieldsPreserveConfigurationErrors(t *testing.T) {
	data := map[string]interface{}{"extra": true}
	v := NewValidator().SetRules(map[string]string{"name": "required"})
	if _, err := v.Validate(data, DisallowUnknownFields(), WithScene("missing")); !errors.Is(err, ErrUnknownScene) {
		t.Fatalf("unknown scene identity changed: %v", err)
	}
	v.SetRules(map[string]string{"name": "not_a_registered_rule"})
	if _, err := v.Validate(data, DisallowUnknownFields()); !errors.Is(err, ErrUnknownRule) {
		t.Fatalf("invalid rule identity changed: %v", err)
	}
	var zero Validator
	for _, input := range []map[string]interface{}{nil, {}} {
		result, err := zero.Validate(input, DisallowUnknownFields(), DisallowUnknownFields())
		if err != nil || !result.Valid() {
			t.Fatalf("empty strict input rejected: %v %v", result, err)
		}
	}
}

func TestIssue37StrictFieldsDoNotPolluteSharedRuleCache(t *testing.T) {
	rules := map[string]string{"name": "required"}
	data := map[string]interface{}{"name": "ok", "extra": true}
	for repeat := 0; repeat < 20; repeat++ {
		strict, err := ValidateRules(data, rules, DisallowUnknownFields())
		if err != nil || strict.Valid() {
			t.Fatalf("cached strict validation accepted extra field: %v", err)
		}
		legacy, err := ValidateRules(data, rules)
		if err != nil || !legacy.Valid() {
			t.Fatalf("strict option leaked into cached validator: %v", err)
		}
	}
}

func TestIssue37StrictFieldsAreTopLevelOnly(t *testing.T) {
	v := NewValidator().SetRules(map[string]string{"profile": "required"})
	nested := map[string]interface{}{"display_name": "name", "is_admin": true}
	data := map[string]interface{}{"profile": nested}
	result, err := v.Validate(data, DisallowUnknownFields())
	if err != nil || !result.Valid() {
		t.Fatalf("top-level policy unexpectedly traversed nested data: %v", err)
	}
	if nested["is_admin"] != true || len(nested) != 2 || len(data) != 1 {
		t.Fatal("validation filtered input")
	}
}

func TestIssue37UnknownFieldsUseConfiguredMessages(t *testing.T) {
	v := NewValidator().SetMessages(map[string]string{
		"extra.unknown_field": "unexpected {:field}",
		"unknown_field":       "blocked {:field}",
	})
	result, err := v.Validate(map[string]interface{}{"extra": true, "other": true}, DisallowUnknownFields(), CollectAllErrors())
	if err != nil {
		t.Fatal(err)
	}
	messages := result.Errors()
	if len(messages) != 2 || messages[0] != "unexpected extra" || messages[1] != "blocked other" {
		t.Fatalf("message customization not applied: %v", messages)
	}
}
