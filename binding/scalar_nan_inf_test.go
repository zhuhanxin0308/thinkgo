package binding

import (
	"math"
	"reflect"
	"testing"
)

func TestParseFiniteFloatRejectsNonFiniteAndInvalidValues(t *testing.T) {
	validCases := []struct {
		text    string
		bitSize int
		want    float64
	}{
		{text: "0", bitSize: 64, want: 0},
		{text: "-0", bitSize: 64, want: 0},
		{text: "19.99", bitSize: 64, want: 19.99},
		{text: "-3.5", bitSize: 32, want: -3.5},
		{text: "5e-324", bitSize: 64, want: 5e-324},
	}
	for _, tc := range validCases {
		got, err := ParseFiniteFloat(tc.text, tc.bitSize)
		if err != nil || got != tc.want {
			t.Fatalf("ParseFiniteFloat(%q, %d) = (%v, %v), want (%v, nil)", tc.text, tc.bitSize, got, err, tc.want)
		}
	}

	invalidCases := []struct {
		text    string
		bitSize int
	}{
		{text: "NaN", bitSize: 64},
		{text: "nan", bitSize: 64},
		{text: "+NaN", bitSize: 64},
		{text: "-NaN", bitSize: 64},
		{text: "Inf", bitSize: 64},
		{text: "+Inf", bitSize: 64},
		{text: "-Inf", bitSize: 64},
		{text: "Infinity", bitSize: 64},
		{text: "+Infinity", bitSize: 64},
		{text: "-Infinity", bitSize: 64},
		{text: "NaN", bitSize: 32},
		{text: "Inf", bitSize: 32},
		{text: "-Inf", bitSize: 32},
		{text: "3.5e38", bitSize: 32},
		{text: "1e309", bitSize: 64},
		{text: "", bitSize: 64},
		{text: " 1.25", bitSize: 64},
		{text: "1.25 ", bitSize: 64},
		{text: "not-a-float", bitSize: 64},
		{text: "1.5", bitSize: 16},
	}
	for _, tc := range invalidCases {
		if got, err := ParseFiniteFloat(tc.text, tc.bitSize); err == nil {
			t.Fatalf("ParseFiniteFloat(%q, %d) 应拒绝非法或非有限浮点数，实际返回 %v", tc.text, tc.bitSize, got)
		}
	}
}

func TestScalarHelpersRejectNaNAndInfValues(t *testing.T) {
	for _, nonFinite := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		var target64 float64
		if assignScalar(reflect.ValueOf(&target64).Elem(), nonFinite) {
			t.Fatalf("assignScalar 不应接受非有限 float64: %v", nonFinite)
		}
		var target32 float32
		if assignScalar(reflect.ValueOf(&target32).Elem(), float32(nonFinite)) {
			t.Fatalf("assignScalar 不应接受非有限 float32: %v", nonFinite)
		}
		if text, ok := scalarNumber(nonFinite); ok || text != "" {
			t.Fatalf("scalarNumber 不应格式化非有限浮点数: (%q, %v)", text, ok)
		}
		val := reflect.ValueOf(nonFinite)
		if num, ok := assignedFloatValue(&node{typ: val.Type()}, val); ok || num != "" {
			t.Fatalf("assignedFloatValue 不应接受非有限浮点数: (%q, %v)", num, ok)
		}
	}
}
