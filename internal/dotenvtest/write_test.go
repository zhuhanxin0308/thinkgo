package dotenvtest_test

import (
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/env"
	"github.com/zhuhanxin0308/thinkgo/v3/internal/dotenvtest"
)

func TestPrivateFixtureIsAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := dotenvtest.WriteFile(path, []byte("MODE=fixture\n")); err != nil {
		t.Fatal(err)
	}
	loaded := env.NewEnv()
	if err := loaded.Load(path); err != nil || loaded.FileValues()["MODE"] != "fixture" {
		t.Fatalf("私有夹具应可加载: %v", err)
	}
}
