package driver

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkGoTemplateCachedDisplay 测量已预热的小模板与多段模板，固定数据与独立输出目标。
func BenchmarkGoTemplateCachedDisplay(b *testing.B) {
	for _, test := range []struct {
		name   string
		copies int
	}{{"small", 1}, {"large", 64}} {
		b.Run(test.name, func(b *testing.B) {
			directory := b.TempDir()
			content := strings.Repeat(`<article><h2>{{.Title}}</h2><p>{{lang "hello"}}: {{.Body}}</p></article>`, test.copies)
			if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte(content), 0o600); err != nil {
				b.Fatal(err)
			}
			driver := NewGoTemplate()
			if err := driver.Config(map[string]interface{}{"view_path": directory}); err != nil {
				b.Fatal(err)
			}
			if err := driver.SetFuncMap(map[string]interface{}{"lang": func(key string) string { return key }}); err != nil {
				b.Fatal(err)
			}
			data := map[string]interface{}{"Title": "页面标题", "Body": "正文内容"}
			if err := driver.Display(io.Discard, "index", data); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := driver.Display(io.Discard, "index", data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
