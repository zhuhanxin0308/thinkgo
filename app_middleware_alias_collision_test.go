package framework

import (
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// TestMiddlewareAliasesRejectNormalizedCollisions 验证冲突在发布任何别名前被确定地拒绝。
func TestMiddlewareAliasesRejectNormalizedCollisions(t *testing.T) {
	for _, target := range []string{"first", "second"} {
		t.Run(target, func(t *testing.T) {
			pipeline := middleware.NewPipeline()
			pipeline.Alias("first", func(request *context.Request, next func(*context.Request) *context.Response) *context.Response {
				return next(request)
			})
			pipeline.Alias("second", func(request *context.Request, next func(*context.Request) *context.Response) *context.Response {
				return next(request)
			})
			app := &App{config: config.NewConfig(), middleware: pipeline}
			if err := app.config.Set("middleware.alias", map[string]interface{}{
				"gate": "first", " gate ": target, "other": "second",
			}); err != nil {
				t.Fatal(err)
			}
			app.applyMiddlewareConfig()
			err := app.StartupError()
			if err == nil || !strings.Contains(err.Error(), "重复别名") || !strings.Contains(err.Error(), "gate") {
				t.Fatalf("规范化冲突必须产生明确启动错误: %v", err)
			}
			if pipeline.ResolveAlias("gate") != nil || pipeline.ResolveAlias("other") != nil {
				t.Fatal("冲突配置不得部分发布别名")
			}
			if pipeline.ResolveAlias("first") == nil || pipeline.ResolveAlias("second") == nil {
				t.Fatal("冲突不得破坏已注册处理器")
			}
		})
	}
}

// TestMiddlewareAliasesPreserveNormalizedChains 验证无歧义别名链与优先级继续使用规范名称。
func TestMiddlewareAliasesPreserveNormalizedChains(t *testing.T) {
	pipeline := middleware.NewPipeline()
	pipeline.Alias("base", func(request *context.Request, next func(*context.Request) *context.Response) *context.Response {
		return next(request)
	})
	app := &App{config: config.NewConfig(), middleware: pipeline}
	if err := app.config.Set("middleware", map[string]interface{}{
		"alias":    map[string]interface{}{" first ": " second ", " second ": "base"},
		"priority": []string{"first", "second"},
	}); err != nil {
		t.Fatal(err)
	}
	app.applyMiddlewareConfig()
	if err := app.StartupError(); err != nil {
		t.Fatal(err)
	}
	if pipeline.ResolveAlias("first") == nil || pipeline.ResolveAlias("second") == nil {
		t.Fatal("规范化别名链未完整发布")
	}
}
