package framework

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/event"
)

type applicationLoaderService struct {
	Service
	order  *[]string
	marker *string
}

func (service *applicationLoaderService) Register() error {
	*service.order = append(*service.order, "service_register")
	*service.marker = service.App().Config().GetString("app.marker")
	return nil
}

// TestApplicationLoaderMatchesThinkPHPLoadOrder 验证项目定义在配置加载后、
// AppInit 之前装配，避免业务服务依赖尚未生效的配置。
func TestApplicationLoaderMatchesThinkPHPLoadOrder(t *testing.T) {
	basePath := t.TempDir()
	configPath := filepath.Join(basePath, "config")
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatalf("创建配置目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configPath, "app.json"), []byte(`{
  "marker": "loaded-before-service",
  "default_timezone": "Asia/Shanghai",
  "with_route": true
}`), 0o644); err != nil {
		t.Fatalf("写入应用配置失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configPath, "log.json"), []byte(`{
  "default": "file",
  "level": [],
  "type_channel": {},
  "close": true,
  "processor": null,
  "channels": {}
}`), 0o644); err != nil {
		t.Fatalf("写入日志配置失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configPath, "cache.json"), []byte(`{
  "default": "memory",
  "stores": {
    "memory": {
      "type": "memory",
      "max_entries": 128,
      "prefix": "",
      "expire": 0,
      "tag_prefix": "tag:",
      "serialize": []
    }
  }
}`), 0o644); err != nil {
		t.Fatalf("写入缓存配置失败: %v", err)
	}

	application := NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = application.Close() })
	order := make([]string, 0, 3)
	marker := ""
	initializedDuringLoad := false
	service := &applicationLoaderService{order: &order, marker: &marker}
	if err := application.RegisterApplicationLoader(func(current *App) error {
		initializedDuringLoad = current.Initialized()
		order = append(order, "application_load")
		if err := current.LoadEvent(EventDefinition{Listen: map[string][]event.Listener{
			event.EventAppInit: {
				&event.SimpleListener{Handler: func(event.Event) error {
					order = append(order, "app_init")
					return nil
				}},
			},
		}}); err != nil {
			return err
		}
		return current.Register(service)
	}); err != nil {
		t.Fatalf("注册应用定义加载器失败: %v", err)
	}

	if err := application.Initialize(); err != nil {
		t.Fatalf("初始化应用失败: %v", err)
	}
	if marker != "loaded-before-service" {
		t.Fatalf("服务注册阶段未读取到最终配置: %q", marker)
	}
	if !initializedDuringLoad {
		t.Fatal("ThinkPHP 在加载 app 定义前已经把应用标记为 initialized")
	}
	wantOrder := []string{"application_load", "service_register", "app_init"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("应用定义生命周期错误: want=%v got=%v", wantOrder, order)
	}
}

// TestApplicationLoaderCanOnlyBeRegisteredOnce 验证兼容装配只有一个项目定义入口，
// 重复调用生成的 Register 不会重复装配业务组件。
func TestApplicationLoaderCanOnlyBeRegisteredOnce(t *testing.T) {
	application := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = application.Close() })
	loader := func(*App) error { return nil }
	if err := application.RegisterApplicationLoader(loader); err != nil {
		t.Fatalf("首次注册应用定义加载器失败: %v", err)
	}
	if err := application.RegisterApplicationLoader(loader); !errors.Is(err, ErrDuplicateRegistration) {
		t.Fatalf("重复应用定义加载器错误不正确: %v", err)
	}
}
