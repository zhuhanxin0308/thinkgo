package httpheader

import "testing"

func TestMergeVaryPreservesOrderAndWildcard(t *testing.T) {
	for _, test := range []struct {
		name   string
		groups [][]string
		want   string
	}{
		{"empty", [][]string{{"", ", "}}, ""},
		{"duplicates", [][]string{{"Origin, Accept-Encoding"}, {"origin", "Accept-Language", "accept-encoding"}}, "Origin, Accept-Encoding, Accept-Language"},
		{"wildcard", [][]string{{"Accept-Encoding"}, {"Origin, *"}}, "*"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := MergeVary(test.groups...); got != test.want {
				t.Errorf("合并 Vary 得到 %q，期望 %q", got, test.want)
			}
		})
	}
}
