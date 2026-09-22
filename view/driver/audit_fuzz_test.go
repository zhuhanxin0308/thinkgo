package driver

import (
	"bytes"
	"html/template"
	"os"
	"path/filepath"
	"testing"
)

// FuzzTemplateEscapingDifferential 核对文本、属性及 URL 三种上下文的转义与标准库一致，复用缓存不得串值。
func FuzzTemplateEscapingDifferential(f *testing.F) {
	for _, value := range []string{"", "<script>alert(1)</script>", "javascript:alert(1)", "\" onload=evil"} {
		f.Add(value)
	}
	const source = `<p>{{.Value}}</p><a href="{{.Value}}" title="{{.Value}}">link</a>`
	directory := f.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "audit.html"), []byte(source), 0o600); err != nil {
		f.Fatal(err)
	}
	driver := NewGoTemplate()
	if err := driver.Config(map[string]interface{}{"view_path": directory, "view_suffix": "html", "cache": true}); err != nil {
		f.Fatal(err)
	}
	reference := template.Must(template.New("audit").Parse(source))
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 4096 {
			return
		}
		for _, current := range []string{value, "clean"} {
			data := map[string]interface{}{"Value": current}
			actual, err := driver.Fetch("audit", data)
			if err != nil {
				t.Fatal(err)
			}
			var expected bytes.Buffer
			if err := reference.Execute(&expected, data); err != nil {
				t.Fatal(err)
			}
			if actual != expected.String() {
				t.Fatal("模板转义或缓存请求隔离与参考实现不一致")
			}
		}
	})
}
