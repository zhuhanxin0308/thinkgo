package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type environmentLookupFunc func(string) (string, bool)

func (lookup environmentLookupFunc) Lookup(name string) (string, bool) { return lookup(name) }

// applyEnvironmentBounded 将锁重入回归限制在单次测试内，避免挂住测试进程。
func applyEnvironmentBounded(t *testing.T, cfg *Config, source EnvironmentSource) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cfg.ApplyEnvironment(source) }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("环境来源回调发生锁重入死锁")
		return nil
	}
}

func TestApplyEnvironmentSourceCanReadConfiguration(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Set("app.port", "8080"); err != nil {
		t.Fatal(err)
	}
	err := applyEnvironmentBounded(t, cfg, environmentLookupFunc(func(name string) (string, bool) {
		return cfg.GetString("app.port") + "-env", cfg.Has("app.port")
	}))
	if err != nil || cfg.GetString("app.port") != "8080-env" {
		t.Fatalf("回读配置后的覆盖失败: %v", err)
	}
}

func TestApplyEnvironmentDoesNotOverwriteSourceMutation(t *testing.T) {
	cfg := NewConfig()
	if err := cfg.Set("app.port", "8080"); err != nil {
		t.Fatal(err)
	}
	err := applyEnvironmentBounded(t, cfg, environmentLookupFunc(func(name string) (string, bool) {
		if err := cfg.Set("concurrent.value", "preserved"); err != nil {
			return "", false
		}
		return "9090", true
	}))
	if !errors.Is(err, ErrConfigEnvironmentConflict) {
		t.Fatal("过期的配置快照应返回提交冲突")
	}
	if cfg.GetString("app.port") != "8080" || cfg.GetString("concurrent.value") != "preserved" {
		t.Fatal("过期快照覆盖了并发配置或发布了部分环境值")
	}
}

func TestApplyEnvironmentReentrantTypeFailureIsAtomic(t *testing.T) {
	cfg := NewConfig()
	for key, value := range map[string]interface{}{"app.name": "original", "app.port": 8080} {
		if err := cfg.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	err := applyEnvironmentBounded(t, cfg, environmentLookupFunc(func(name string) (string, bool) {
		return cfg.GetString("app.name"), true
	}))
	if !errors.Is(err, ErrConfigEnvironmentType) || cfg.GetInt("app.port") != 8080 {
		t.Fatalf("类型错误未原子回滚: %v", err)
	}
}

// TestApplyEnvironmentDetectsConcurrentWriters 验证各写入入口都会使旧覆盖计划失效。
func TestApplyEnvironmentDetectsConcurrentWriters(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "concurrent.json")
	if err := os.WriteFile(configPath, []byte(`{"value":"preserved"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config) error{
		"Set":  func(cfg *Config) error { return cfg.Set("concurrent.value", "preserved") },
		"Load": func(cfg *Config) error { return cfg.Load(configPath, "concurrent") },
		"Replace": func(cfg *Config) error {
			replacement := cfg.Clone()
			if err := replacement.Set("concurrent.value", "preserved"); err != nil {
				return err
			}
			cfg.ReplaceWithSnapshot(replacement)
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := NewConfig()
			if err := cfg.Set("app.port", "8080"); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			done := make(chan error, 1)
			go func() {
				done <- cfg.ApplyEnvironment(environmentLookupFunc(func(string) (string, bool) {
					close(entered)
					<-release
					return "9090", true
				}))
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("未进入来源查询")
			}
			if err := mutate(cfg); err != nil {
				t.Fatal(err)
			}
			release <- struct{}{}
			select {
			case err := <-done:
				if !errors.Is(err, ErrConfigEnvironmentConflict) {
					t.Fatalf("未识别并发写入: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("提交未返回")
			}
			if cfg.GetString("app.port") != "8080" || cfg.GetString("concurrent.value") != "preserved" {
				t.Fatal("并发变更丢失或环境值部分提交")
			}
		})
	}
}
