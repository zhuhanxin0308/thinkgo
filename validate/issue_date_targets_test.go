package validate_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/validate"
)

func requireIssue137DateResult(t *testing.T, result validate.Result, err error, valid bool, rule, parameter string) {
	t.Helper()
	if err != nil || result.Valid() != valid {
		t.Fatalf("%s:%s: valid=%t want=%t violations=%+v err=%v", rule, parameter, result.Valid(), valid, result.Violations(), err)
	}
	if !valid {
		violations := result.Violations()
		if len(violations) != 1 || violations[0].Field != "booking" || violations[0].Rule != rule || violations[0].Param != parameter {
			t.Fatalf("date target failure lost its public rule identity: %+v", violations)
		}
	}
}

func TestIssue137DateLiteralIgnoresSameNamedRequestValues(t *testing.T) {
	const cutoff = "2026-01-01"
	var nilString *string
	pointed := "2099-01-01"
	shadows := []struct {
		name  string
		value any
	}{
		{"later_date", "2099-01-01"},
		{"earlier_date", "1900-01-01"},
		{"invalid_text", "not-a-date"},
		{"integer", 2099},
		{"float", 2099.5},
		{"json_number", json.Number("2099")},
		{"boolean", true},
		{"time", time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"pointer", &pointed},
		{"slice", []string{"2099-01-01"}},
		{"map", map[string]any{"date": "2099-01-01"}},
		{"struct", struct{ Date string }{Date: "2099-01-01"}},
		{"nil", nil},
		{"typed_nil", nilString},
	}
	for _, direction := range []struct {
		name, inside, outside string
	}{
		{"before", "2025-01-01", "2027-01-01"},
		{"after", "2027-01-01", "2025-01-01"},
	} {
		rules := map[string]string{"booking": direction.name + ":" + cutoff}
		for _, input := range []struct {
			name, value string
			valid       bool
		}{
			{"inside", direction.inside, true},
			{"outside", direction.outside, false},
			{"equal", cutoff, false},
		} {
			t.Run(direction.name+"/"+input.name, func(t *testing.T) {
				data := map[string]any{"booking": input.value}
				result, err := validate.ValidateRules(data, rules)
				requireIssue137DateResult(t, result, err, input.valid, direction.name, cutoff)
				data["unrelated_note"] = "2099-01-01"
				result, err = validate.ValidateRules(data, rules)
				requireIssue137DateResult(t, result, err, input.valid, direction.name, cutoff)
				for _, shadow := range shadows {
					t.Run(shadow.name, func(t *testing.T) {
						data[cutoff] = shadow.value
						result, err := validate.ValidateRules(data, rules)
						requireIssue137DateResult(t, result, err, input.valid, direction.name, cutoff)
					})
				}
			})
		}
	}
}

func TestIssue137DateLiteralLayoutsAndExclusiveBoundaries(t *testing.T) {
	for _, format := range []struct {
		name, parameter, specification, earlier, equal, later string
	}{
		{"date", "2026-01-01", "2026-01-01", "2025-12-31", "2026-01-01", "2026-01-02"},
		{"local_datetime", "2026-01-01 12:00:00", `"2026-01-01 12:00:00"`, "2026-01-01 11:59:59", "2026-01-01 12:00:00", "2026-01-01 12:00:01"},
		{"rfc3339_nano", "2026-01-01T12:00:00.123456789+02:00", "2026-01-01T12:00:00.123456789+02:00", "2026-01-01T10:00:00.123456788Z", "2026-01-01T10:00:00.123456789Z", "2026-01-01T10:00:00.123456790Z"},
	} {
		for _, rule := range []struct {
			name  string
			after bool
		}{
			{"before", false}, {"beforeDate", false}, {"after", true}, {"afterDate", true},
		} {
			t.Run(format.name+"/"+rule.name, func(t *testing.T) {
				validator := validate.NewValidator().SetRules(map[string]string{"booking": rule.name + ":" + format.specification})
				for _, input := range []struct {
					value string
					valid bool
				}{
					{format.earlier, !rule.after}, {format.equal, false}, {format.later, rule.after}, {"not-a-date", false},
				} {
					result, err := validator.Validate(map[string]any{"booking": input.value, format.parameter: "2099-01-01"})
					requireIssue137DateResult(t, result, err, input.valid, rule.name, format.parameter)
				}
			})
		}
	}
}

func TestIssue137DateFieldReferencesRemainExplicit(t *testing.T) {
	for _, parameter := range []string{"deadline", "2026-02-30"} {
		for _, rule := range []struct {
			name, inside, outside string
		}{
			{"before", "2025-01-01", "2027-01-01"},
			{"beforeField", "2025-01-01", "2027-01-01"},
			{"after", "2027-01-01", "2025-01-01"},
			{"afterField", "2027-01-01", "2025-01-01"},
		} {
			t.Run(parameter+"/"+rule.name, func(t *testing.T) {
				validator := validate.NewValidator().SetRules(map[string]string{"booking": rule.name + ":" + parameter})
				for _, input := range []struct {
					value string
					valid bool
				}{
					{rule.inside, true}, {rule.outside, false}, {"2026-01-01", false},
				} {
					result, err := validator.Validate(map[string]any{"booking": input.value, parameter: "2026-01-01"})
					requireIssue137DateResult(t, result, err, input.valid, rule.name, parameter)
				}
				data := map[string]any{"booking": rule.inside}
				result, err := validator.Validate(data)
				requireIssue137DateResult(t, result, err, false, rule.name, parameter)
				for _, invalid := range []any{nil, "not-a-date", true, []string{"2026-01-01"}} {
					data[parameter] = invalid
					result, err := validator.Validate(data)
					requireIssue137DateResult(t, result, err, false, rule.name, parameter)
				}
			})
		}
	}
}

func TestIssue137ExplicitDateShapedFieldNames(t *testing.T) {
	for _, parameter := range []string{"2026-01-01", "2026-01-01 12:00:00", "2026-01-01T12:00:00.123Z"} {
		for _, rule := range []struct {
			name, value, passingTarget, failingTarget string
		}{
			{"beforeField", "2027-01-01", "2099-01-01", "2000-01-01"},
			{"afterField", "2025-01-01", "2000-01-01", "2099-01-01"},
		} {
			t.Run(rule.name+"/"+parameter, func(t *testing.T) {
				validator := validate.NewValidator().SetRules(map[string]string{"booking": rule.name + ":" + parameter})
				data := map[string]any{"booking": rule.value, parameter: rule.passingTarget}
				result, err := validator.Validate(data)
				requireIssue137DateResult(t, result, err, true, rule.name, parameter)
				data[parameter] = rule.failingTarget
				result, err = validator.Validate(data)
				requireIssue137DateResult(t, result, err, false, rule.name, parameter)
				delete(data, parameter)
				result, err = validator.Validate(data)
				requireIssue137DateResult(t, result, err, false, rule.name, parameter)
			})
		}
	}
}

func TestIssue137DateTargetsUseCallLocationWithCachedPlans(t *testing.T) {
	east := time.FixedZone("issue137-east", 8*60*60)
	for _, rule := range []struct {
		name, parameter string
		utcValid        bool
	}{
		{"before", "2026-01-01", true}, {"beforeDate", "2026-01-01", true}, {"beforeField", "deadline", true},
		{"after", "2026-01-01", false}, {"afterDate", "2026-01-01", false}, {"afterField", "deadline", false},
	} {
		t.Run(rule.name, func(t *testing.T) {
			rules := map[string]string{"booking": rule.name + ":" + rule.parameter}
			data := map[string]any{"booking": "2025-12-31T20:00:00Z", "deadline": "2026-01-01"}
			first := validate.NewValidator().SetRules(rules).SetLocation(time.UTC)
			second := validate.NewValidator().SetRules(rules).SetLocation(east)
			for repeat := 0; repeat < 2; repeat++ {
				result, err := first.Validate(data)
				requireIssue137DateResult(t, result, err, rule.utcValid, rule.name, rule.parameter)
				result, err = second.Validate(data)
				requireIssue137DateResult(t, result, err, !rule.utcValid, rule.name, rule.parameter)
				result, err = validate.ValidateRules(data, rules, validate.WithLocation(east))
				requireIssue137DateResult(t, result, err, !rule.utcValid, rule.name, rule.parameter)
				result, err = validate.ValidateRules(data, rules)
				requireIssue137DateResult(t, result, err, rule.utcValid, rule.name, rule.parameter)
			}
			first.SetLocation(east)
			result, err := first.Validate(data)
			requireIssue137DateResult(t, result, err, !rule.utcValid, rule.name, rule.parameter)
			result, err = first.Validate(data, validate.WithLocation(time.UTC))
			requireIssue137DateResult(t, result, err, rule.utcValid, rule.name, rule.parameter)
			result, err = first.Validate(data)
			requireIssue137DateResult(t, result, err, !rule.utcValid, rule.name, rule.parameter)
		})
	}
}

func TestIssue137DateTargetSceneAndStrictOptionsDoNotChangeMeaning(t *testing.T) {
	const cutoff = "2026-01-01"
	rules := map[string]string{"booking": "before:" + cutoff, "note": "required"}
	validator := validate.NewValidator().SetRules(rules).SetScenes(map[string][]string{"booking_only": {"booking"}})
	data := map[string]any{"booking": "2027-01-01", cutoff: "2099-01-01"}
	for repeat := 0; repeat < 3; repeat++ {
		result, err := validator.Validate(data, validate.WithScene("booking_only"))
		requireIssue137DateResult(t, result, err, false, "before", cutoff)
		result, err = validator.Validate(data, validate.WithScene("booking_only"), validate.DisallowUnknownFields(), validate.CollectAllErrors())
		violations := result.Violations()
		if err != nil || len(violations) != 2 || violations[0].Field != cutoff || violations[0].Rule != "unknown_field" || violations[1].Field != "booking" || violations[1].Rule != "before" {
			t.Fatalf("strict mode must report both the unknown key and the unchanged date failure: %+v %v", violations, err)
		}
		data["booking"] = "2025-01-01"
		result, err = validator.Validate(data, validate.WithScene("booking_only"))
		requireIssue137DateResult(t, result, err, true, "before", cutoff)
		data["booking"] = "2027-01-01"
	}
	validator.SetRules(map[string]string{"booking": "beforeField:" + cutoff, "note": "required"})
	result, err := validator.Validate(data, validate.WithScene("booking_only"))
	requireIssue137DateResult(t, result, err, true, "beforeField", cutoff)
	validator.SetRules(rules)
	result, err = validator.Validate(data, validate.WithScene("booking_only"))
	requireIssue137DateResult(t, result, err, false, "before", cutoff)
}

func TestIssue137ExplicitDateRulesRejectInvalidDefinitions(t *testing.T) {
	for _, name := range []string{"beforeDate", "afterDate"} {
		for _, suffix := range []string{"", ":", ":not-a-date", ":2026-02-30", ":2026-13-01", ":2026-01-01 25:00:00", ":2026-01-01T12:00:00+99:00"} {
			specification := name + suffix
			t.Run(specification, func(t *testing.T) {
				// The protected field is absent, so this must be a definition error,
				// not a skipped optional field or a user-data violation.
				result, err := validate.ValidateRules(nil, map[string]string{"booking": specification})
				if !errors.Is(err, validate.ErrInvalidRule) || !result.Valid() {
					t.Fatalf("invalid date definition accepted: result=%+v err=%v", result.Violations(), err)
				}
				if _, err := validate.DescribeRules(specification); !errors.Is(err, validate.ErrInvalidRule) {
					t.Fatalf("description parser accepted invalid date definition: %v", err)
				}
			})
		}
	}
	for _, name := range []string{"beforeField", "afterField"} {
		if _, err := validate.ValidateRules(nil, map[string]string{"booking": name + ":"}); !errors.Is(err, validate.ErrInvalidRule) {
			t.Fatalf("empty field reference accepted: %s: %v", name, err)
		}
	}
}

func TestIssue137DateRuleMetadataRetainsDeclaredNames(t *testing.T) {
	const cutoff = "2026-01-01"
	for _, rule := range []struct{ name, value string }{
		{"before", "2027-01-01"}, {"beforeDate", "2027-01-01"}, {"beforeField", "2027-01-01"},
		{"after", "2025-01-01"}, {"afterDate", "2025-01-01"}, {"afterField", "2025-01-01"},
	} {
		t.Run(rule.name, func(t *testing.T) {
			specification := rule.name + `:"` + cutoff + `"`
			descriptions, err := validate.DescribeRules(specification)
			if err != nil || len(descriptions) != 1 || descriptions[0].Name != rule.name || descriptions[0].Parameter != cutoff {
				t.Fatalf("declared rule description changed: %+v %v", descriptions, err)
			}
			validator := validate.NewValidator().SetRules(map[string]string{"booking|预订日期": specification}).
				SetMessages(map[string]string{"booking." + rule.name: "{:rule}:{:param}"})
			result, err := validator.Validate(map[string]any{"booking": rule.value, cutoff: cutoff})
			requireIssue137DateResult(t, result, err, false, rule.name, cutoff)
			if result.FirstError() != rule.name+":"+cutoff || result.Violations()[0].Alias != "预订日期" {
				t.Fatalf("rule-specific message or alias changed: %+v", result.Violations())
			}
		})
	}
}
