package binding

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
	"github.com/zhuhanxin0308/thinkgo/v3/validate"
)

func issue137DateRequest(t *testing.T, source string, values map[string]string) *fwcontext.Request {
	t.Helper()
	var body, contentType string
	if source == "form" {
		form := make(url.Values, len(values))
		for name, value := range values {
			form.Set(name, value)
		}
		body, contentType = form.Encode(), "application/x-www-form-urlencoded"
	} else {
		encoded, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		body, contentType = string(encoded), "application/json"
	}
	raw := httptest.NewRequest(http.MethodPost, "/booking", strings.NewReader(body))
	raw.Header.Set("Content-Type", contentType)
	return fwcontext.MustNewRequest(raw)
}

func requireIssue137BindingFailure(t *testing.T, err error, status int, field, rule string) {
	t.Helper()
	var failure *exception.HttpException
	if !errors.As(err, &failure) || failure.StatusCode != status {
		t.Fatalf("expected HTTP %d validation result, got %v", status, err)
	}
	issues, ok := failure.Data["errors"].([]Issue)
	if !ok {
		t.Fatalf("binding issues missing: %#v", failure.Data)
	}
	for _, issue := range issues {
		if issue.Field == field && issue.Rule == rule {
			return
		}
	}
	t.Fatalf("missing %s/%s failure: %+v", field, rule, issues)
}

func TestIssue137BindDateTargetMeaningAndAtomicity(t *testing.T) {
	const cutoff = "2026-01-01"
	for _, source := range []string{"json", "form"} {
		path := "body.booking"
		if source == "form" {
			path = "form.booking"
		}
		for _, rule := range []struct {
			name, passingValue, passingShadow, failingValue, failingShadow string
		}{
			{"before", "2025-01-01", "not-a-date", "2027-01-01", "2099-01-01"},
			{"beforeDate", "2025-01-01", "not-a-date", "2027-01-01", "2099-01-01"},
			{"after", "2027-01-01", "not-a-date", "2025-01-01", "2000-01-01"},
			{"afterDate", "2027-01-01", "not-a-date", "2025-01-01", "2000-01-01"},
			{"beforeField", "2027-01-01", "2099-01-01", "2025-01-01", "2000-01-01"},
			{"afterField", "2025-01-01", "2000-01-01", "2027-01-01", "2099-01-01"},
		} {
			typ := reflect.StructOf([]reflect.StructField{
				{Name: "Booking", Type: reflect.TypeFor[string](), Tag: reflect.StructTag(source + `:"booking" validate:"` + rule.name + ":" + cutoff + `"`)},
				{Name: "Shadow", Type: reflect.TypeFor[string](), Tag: reflect.StructTag(source + `:"` + cutoff + `" validate:"required"`)},
				{Name: "Note", Type: reflect.TypeFor[string](), Tag: reflect.StructTag(source + `:"note" validate:"required"`)},
			})
			for _, input := range []struct {
				name, value, shadow string
				valid               bool
			}{
				{"passing", rule.passingValue, rule.passingShadow, true},
				{"failing", rule.failingValue, rule.failingShadow, false},
				{"equal", cutoff, cutoff, false},
			} {
				t.Run(source+"/"+rule.name+"/"+input.name, func(t *testing.T) {
					target := reflect.New(typ)
					target.Elem().Field(0).SetString("original-booking")
					target.Elem().Field(1).SetString("original-shadow")
					target.Elem().Field(2).SetString("original-note")
					original := target.Elem().Interface()
					request := issue137DateRequest(t, source, map[string]string{
						"booking": input.value, cutoff: input.shadow, "note": "changed-note",
					})
					err := Bind(request, target.Interface())
					if input.valid {
						if err != nil || target.Elem().Field(0).String() != input.value || target.Elem().Field(1).String() != input.shadow || target.Elem().Field(2).String() != "changed-note" {
							t.Fatalf("valid date request did not bind completely: target=%v err=%v", target.Elem().Interface(), err)
						}
						return
					}
					requireIssue137BindingFailure(t, err, http.StatusUnprocessableEntity, path, rule.name)
					if !reflect.DeepEqual(original, target.Elem().Interface()) {
						t.Fatalf("date validation failure modified the target: %v", target.Elem().Interface())
					}
				})
			}
		}
	}
}

func TestIssue137BindStrictDeclaredDateKeyCannotReplaceLiteral(t *testing.T) {
	type input struct {
		Booking string `json:"booking" validate:"before:2026-01-01"`
		Shadow  string `json:"2026-01-01" validate:"required"`
	}
	original := input{Booking: "original-booking", Shadow: "original-shadow"}
	target := original
	err := Bind(issue137DateRequest(t, "json", map[string]string{
		"booking": "2027-01-01", "2026-01-01": "2099-01-01",
	}), &target, validate.DisallowUnknownFields())
	requireIssue137BindingFailure(t, err, http.StatusUnprocessableEntity, "body.booking", "before")
	if target != original {
		t.Fatalf("strict date failure modified target: %+v", target)
	}
}

func TestIssue137BindUnknownDateKeyStillFailsAtomically(t *testing.T) {
	type input struct {
		Booking string `json:"booking" validate:"beforeDate:2026-01-01"`
	}
	original := input{Booking: "original-booking"}
	target := original
	err := Bind(issue137DateRequest(t, "json", map[string]string{
		"booking": "2025-01-01", "2026-01-01": "2099-01-01",
	}), &target)
	requireIssue137BindingFailure(t, err, http.StatusBadRequest, "body.2026-01-01", "unknown")
	if target != original {
		t.Fatalf("unknown date key modified target: %+v", target)
	}
}

func TestIssue137BindInvalidDateLiteralIsDefinitionError(t *testing.T) {
	for _, name := range []string{"beforeDate", "afterDate"} {
		t.Run(name, func(t *testing.T) {
			typ := reflect.StructOf([]reflect.StructField{{
				Name: "Booking", Type: reflect.TypeFor[string](),
				Tag: reflect.StructTag(`json:"booking" validate:"` + name + `:2026-02-30"`),
			}})
			if err := ValidateType(typ); !errors.Is(err, ErrDefinition) {
				t.Fatalf("invalid date literal accepted as a binding definition: %v", err)
			}
			target := reflect.New(typ)
			target.Elem().Field(0).SetString("original-booking")
			err := Bind(issue137DateRequest(t, "json", map[string]string{}), target.Interface())
			if !errors.Is(err, ErrDefinition) || target.Elem().Field(0).String() != "original-booking" {
				t.Fatalf("missing optional input masked an invalid rule or modified target: %v %v", target.Elem().Interface(), err)
			}
		})
	}
}
