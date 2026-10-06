package command

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestIssue17GeneratedProjectEnablesMustRoute(t *testing.T) {
	sources, err := projectSources("example.com/issue17")
	if err != nil {
		t.Fatal(err)
	}
	var configuration map[string]interface{}
	if err := json.Unmarshal(sources[filepath.Join("config", "route.json")], &configuration); err != nil {
		t.Fatal(err)
	}
	if value, exists := configuration["url_route_must"]; !exists || value != true {
		t.Fatalf("generated project must explicitly require routes: %#v", configuration)
	}
	if len(sources[filepath.Join("app", "index", "route", "app.go")]) == 0 {
		t.Fatal("secure skeleton has no explicit routes")
	}
}
