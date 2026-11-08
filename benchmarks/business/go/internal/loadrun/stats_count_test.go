package loadrun

import (
	"math"
	"testing"
)

func TestHistogramCountBounds(t *testing.T) {
	cases := []struct {
		name  string
		total int64
		want  uint64
	}{
		{name: "negative_corrupt_count", total: -1, want: 0},
		{name: "minimum_signed_count", total: math.MinInt64, want: 0},
		{name: "empty_histogram", total: 0, want: 0},
		{name: "single_sample", total: 1, want: 1},
		{name: "maximum_signed_count", total: math.MaxInt64, want: math.MaxInt64},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := histogramCount(testCase.total); got != testCase.want {
				t.Fatalf("直方图计数转换错误: 输入=%d，实际=%d，预期=%d", testCase.total, got, testCase.want)
			}
		})
	}
}
