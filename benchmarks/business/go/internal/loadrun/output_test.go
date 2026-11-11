package loadrun

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveReportPlainAndGzip(t *testing.T) {
	configuration := testConfig()
	start := time.Now()
	report := newStatistics(configuration, start).finish(start.Add(time.Second), time.Second, false, 0)
	for _, name := range []string{"run.json", "run.json.gz"} {
		path := filepath.Join(t.TempDir(), name)
		if err := SaveReport(path, report); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		decoder := json.NewDecoder(file)
		if filepath.Ext(path) == ".gz" {
			compressed, gzipErr := gzip.NewReader(file)
			if gzipErr != nil {
				t.Fatal(gzipErr)
			}
			defer compressed.Close()
			decoder = json.NewDecoder(compressed)
		}
		var restored Report
		if err := decoder.Decode(&restored); err != nil || restored.SchemaVersion != report.SchemaVersion || restored.ValidatorVersion != responseValidatorVersion || restored.Config.Secret != "" {
			t.Fatalf("结果文件不可恢复或泄露密钥: %v", err)
		}
		file.Close()
		if err := SaveReport(path, report); err == nil {
			t.Fatal("覆盖了已有测量结果")
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 1 {
			t.Fatalf("留下临时输出文件: %v %#v", err, entries)
		}
	}
}

func TestSaveReportRejectsInvalidPath(t *testing.T) {
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing", "run.json"), t.TempDir()} {
		if err := SaveReport(path, Report{}); err == nil {
			t.Fatalf("无效输出路径被接受: %s", path)
		}
	}
}
