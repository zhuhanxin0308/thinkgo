package sqllex

import (
	"strings"
	"testing"
)

func TestPostgresDollarDelimiter(t *testing.T) {
	for _, test := range []struct {
		text string
		want string
	}{
		{"$$?$$", "$$"},
		{" $文本9$?$文本9$", "$文本9$"},
		{"($_tag$", "$_tag$"},
		{"($tag_9$", "$tag_9$"},
		{"name$tag$", ""},
		{"名$tag$", ""},
		{"$9tag$", ""},
		{"$bad-tag$", ""},
		{"$unclosed", ""},
		{"$", ""},
	} {
		if got := PostgresDollarDelimiter(test.text, strings.IndexByte(test.text, '$')); got != test.want {
			t.Errorf("美元引号边界错误: %q => %q，期望 %q", test.text, got, test.want)
		}
	}
	for _, index := range []int{-1, 0, 3} {
		if got := PostgresDollarDelimiter("abc", index); got != "" {
			t.Fatalf("无效起始位置被识别: %d => %q", index, got)
		}
	}
}
