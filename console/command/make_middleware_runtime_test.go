package command

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// TestMakeMiddlewareRunsInConsumer 验证真实生成物能接入框架管道，并执行下游调用和缺失下游的错误分支。
func TestMakeMiddlewareRunsInConsumer(t *testing.T) {
	base := t.TempDir()
	writeGeneratorTestModule(t, base)
	command := &MakeMiddleware{}
	command.Configure()
	command.SetApp(&framework.App{BasePath: base})
	if err := command.Execute(console.NewInput("Audit"), generatorTestOutput()); err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFixture(t, base, "app/index/middleware/audit_test.go", generatedMiddlewareRuntimeTest)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	process := exec.CommandContext(ctx, "go", "test", "-mod=mod", "-count=1", "-cover", "./app/index/middleware")
	process.Dir = base
	process.Env = append(os.Environ(), "GOWORK=off")
	output, err := process.CombinedOutput()
	if err != nil {
		t.Fatalf("生成的中间件无法编译或执行: %v\n%s", err, output)
	}
	t.Logf("下游中间件行为与覆盖率: %s", output)
}

// TestMakeMiddlewareRejectsInvalidTargets 验证非法名称和已有用户文件不会被生成器覆盖。
func TestMakeMiddlewareRejectsInvalidTargets(t *testing.T) {
	base := t.TempDir()
	command := &MakeMiddleware{}
	command.SetApp(&framework.App{BasePath: base})
	if err := command.Execute(console.NewInput("../Audit"), generatorTestOutput()); err == nil {
		t.Fatal("中间件生成器接受了越界名称")
	}
	const original = "package middleware\n\n// Audit 是项目已有中间件。\ntype Audit struct{}\n"
	writeDiscoveryFixture(t, base, "app/index/middleware/audit.go", original)
	if err := command.Execute(console.NewInput("Audit"), generatorTestOutput()); !errors.Is(err, os.ErrExist) {
		t.Fatalf("重复生成没有返回文件存在错误: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(base, "app", "index", "middleware", "audit.go"))
	if err != nil || string(content) != original {
		t.Fatalf("生成器修改了已有中间件: 内容=%q 错误=%v", content, err)
	}
}

const generatedMiddlewareRuntimeTest = `package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// ExistingAudit 保留旧版本生成代码使用的公开下游类型，验证升级后无需重建已有文件。
type ExistingAudit struct{}

func (*ExistingAudit) Handle(request *context.Request, next middleware.Next) *context.Response {
	return next(request)
}

// TestAuditMiddleware 验证生成的方法满足当前框架类型，并原样传递请求和响应。
func TestAuditMiddleware(t *testing.T) {
	instance := &Audit{}
	var handler middleware.Handler = instance.Handle
	request, err := context.NewRequest(httptest.NewRequest(http.MethodGet, "/audit", nil))
	if err != nil {
		t.Fatal(err)
	}
	want := context.NewResponse().Code(http.StatusAccepted).Content("已执行下游")
	calls := 0
	response := handler(request, func(actual *context.Request) *context.Response {
		calls++
		if actual != request {
			t.Error("中间件替换了请求实例")
		}
		return want
	})
	if calls != 1 || response != want {
		t.Fatalf("下游调用错误: 次数=%d 响应=%v", calls, response)
	}
	var existing middleware.Handler = (&ExistingAudit{}).Handle
	if response := existing(request, func(*context.Request) *context.Response { return want }); response != want {
		t.Fatal("旧生成代码未能保留现有管道契约")
	}
	response = handler(request, nil)
	if response == nil || response.GetCode() != http.StatusInternalServerError || response.GetContent() != http.StatusText(http.StatusInternalServerError) {
		t.Fatalf("缺少下游时未返回明确错误: %v", response)
	}
}
`
