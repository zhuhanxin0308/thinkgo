package binding

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
)

func TestBindValidatesDeliveredFloatBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, rule, input string
		kind              reflect.Type
		wantError         bool
	}{
		{"float32 rounds up", "lt:1", "0.99999999", reflect.TypeFor[float32](), true},
		{"float32 rounds down", "gt:1", "1.00000001", reflect.TypeFor[float32](), true},
		{"float32 underflows", "gt:0", "1e-100", reflect.TypeFor[float32](), true},
		{"float32 exact binary bound", "max:0.100000001", "0.1", reflect.TypeFor[float32](), true},
		{"float32 between", "between:0,0.100000001", "0.1", reflect.TypeFor[float32](), true},
		{"float32 not between", "notBetween:1,2", "0.99999999", reflect.TypeFor[float32](), true},
		{"float64 rounds up", "lt:1", "0.99999999999999999", reflect.TypeFor[float64](), true},
		{"original input still checked", "elt:1", "1.00000001", reflect.TypeFor[float32](), true},
		{"on exclusive boundary", "lt:1", "1", reflect.TypeFor[float32](), true},
		{"inside boundary", "lt:1", "0.5", reflect.TypeFor[float32](), false},
		{"inclusive boundary", "elt:1", "1", reflect.TypeFor[float32](), false},
		{"smallest float64", "gt:0", "5e-324", reflect.TypeFor[float64](), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			typ := reflect.StructOf([]reflect.StructField{{
				Name: "Value", Type: test.kind,
				Tag: reflect.StructTag(`json:"value" validate:"` + test.rule + `"`),
			}})
			target := reflect.New(typ)
			target.Elem().Field(0).SetFloat(0.25)
			err := Bind(bindingRequest(t, `{"value":`+test.input+`}`), target.Interface())
			if !test.wantError {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var failure *exception.HttpException
			if !errors.As(err, &failure) || failure.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("expected a validation failure, got %v", err)
			}
			if target.Elem().Field(0).Float() != 0.25 {
				t.Fatal("failed binding modified the original target")
			}
			issues := failure.Data["errors"].([]Issue)
			if len(issues) != 1 || issues[0].Field != "body.value" {
				t.Fatalf("expected one field violation, got %#v", issues)
			}
		})
	}
}

func TestBindFloatBoundariesCoverNestedAndOptionalFields(t *testing.T) {
	type item struct {
		Value float32 `json:"value" validate:"lt:1"`
	}
	for _, test := range []struct {
		body, field string
	}{
		{`{"pointer":0.99999999}`, "body.pointer"},
		{`{"optional":0.99999999}`, "body.optional"},
		{`{"nested":{"value":0.99999999}}`, "body.nested.value"},
		{`{"items":[{"value":0.99999999}]}`, "body.items[0].value"},
		{`{"values":{"x":{"value":0.99999999}}}`, `body.values["x"].value`},
	} {
		t.Run(test.field, func(t *testing.T) {
			var target struct {
				Pointer  *float32          `json:"pointer" validate:"lt:1"`
				Optional Optional[float32] `json:"optional" validate:"lt:1"`
				Nested   *item             `json:"nested"`
				Items    []item            `json:"items"`
				Values   map[string]item   `json:"values"`
			}
			err := Bind(bindingRequest(t, test.body), &target)
			var failure *exception.HttpException
			if !errors.As(err, &failure) || failure.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("expected a validation failure, got %v", err)
			}
			issues := failure.Data["errors"].([]Issue)
			if len(issues) != 1 || issues[0].Field != test.field || issues[0].Rule != "lt" {
				t.Fatalf("unexpected nested validation errors: %#v", issues)
			}
			if target.Pointer != nil || target.Optional.IsSet() || target.Nested != nil || target.Items != nil || target.Values != nil {
				t.Fatal("failed binding published a partial object")
			}
		})
	}
}

func TestBindFloatBoundariesPreserveInputRepresentation(t *testing.T) {
	var target struct {
		Value float32 `json:"value" validate:"regex:/^0[.]50$/|lt:1"`
	}
	if err := Bind(bindingRequest(t, `{"value":0.50}`), &target); err != nil || target.Value != 0.5 {
		t.Fatalf("raw numeric representation was lost: value=%v err=%v", target.Value, err)
	}
	if err := Bind(bindingRequest(t, `{"value":0.5}`), &target); err == nil {
		t.Fatal("original representation rule was skipped")
	}
}

func TestBindFloatBoundariesApplyToQueryAndQuotedJSON(t *testing.T) {
	var query struct {
		Value float32 `query:"value" validate:"lt:1"`
	}
	raw := httptest.NewRequest(http.MethodGet, "/?value=0.99999999", nil)
	if err := Bind(fwcontext.MustNewRequest(raw), &query); err == nil {
		t.Fatal("query float rounding bypassed the range rule")
	}
	var quoted struct {
		Value float32 `json:"value,string" validate:"lt:1"`
	}
	if err := Bind(bindingRequest(t, `{"value":"0.99999999"}`), &quoted); err == nil {
		t.Fatal("quoted JSON float rounding bypassed the range rule")
	}
}

func TestValidateTypeRejectsRoundedFloatDefault(t *testing.T) {
	typ := reflect.TypeFor[struct {
		Value float32 `json:"value" default:"0.99999999" validate:"regex:/^0[.]99999999$/|lt:1"`
	}]()
	if err := ValidateType(typ); !errors.Is(err, ErrDefinition) {
		t.Fatalf("rounded default should fail at definition time: %v", err)
	}
}
