package version

import "testing"

func TestVersionLabelsRemainConsistent(t *testing.T) {
	if ProductName == "" || Number == "" || Framework == "" || Console == "" {
		t.Fatalf("版本标签不应为空: product=%q number=%q framework=%q console=%q", ProductName, Number, Framework, Console)
	}
	if Framework != ProductName+" "+Number {
		t.Fatalf("Framework 标签与产品版本不一致: %q", Framework)
	}
	if Console != ProductName+" Framework v"+Number {
		t.Fatalf("Console 标签与产品版本不一致: %q", Console)
	}
}
