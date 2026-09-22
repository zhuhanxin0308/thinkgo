package command

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
	"github.com/zhuhanxin0308/thinkgo/framework/deploy"
)

func deployAuditorWithResult(t *testing.T, level deploy.Level) *deploy.Auditor {
	t.Helper()
	check, err := deploy.NewCheck("custom.readiness", func(context.Context, *framework.App) deploy.Result {
		return deploy.Result{Level: level, Message: "custom result"}
	})
	if err != nil {
		t.Fatalf("创建部署命令测试检查器失败: %v", err)
	}
	auditor, err := deploy.NewAuditor(check)
	if err != nil {
		t.Fatalf("创建部署命令测试审计器失败: %v", err)
	}
	return auditor
}

// TestDeployCheckAuditsAllCompiledApplications 验证默认部署门禁覆盖全部应用，
// 显式 --app 时只审计目标应用，并在输出中保留应用身份。
func TestDeployCheckAuditsAllCompiledApplications(t *testing.T) {
	project := newCommandMultiApplicationProject(t)
	visited := make([]string, 0, 2)
	check, err := deploy.NewCheck("application.identity", func(_ context.Context, current *framework.App) deploy.Result {
		visited = append(visited, current.CurrentApplicationName())
		return deploy.Result{Level: deploy.LevelPass, Message: "ok"}
	})
	if err != nil {
		t.Fatalf("创建多应用部署检查失败: %v", err)
	}
	auditor, err := deploy.NewAuditor(check)
	if err != nil {
		t.Fatalf("创建多应用部署审计器失败: %v", err)
	}
	stdout := &bytes.Buffer{}
	command := &DeployCheck{Command: console.Command{App: project}, auditor: auditor}
	if err := command.Execute(parseMigrationCommandInput(t, command), console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)); err != nil {
		t.Fatalf("审计全部编译期应用失败: %v", err)
	}
	if !reflect.DeepEqual(visited, []string{"admin", "index"}) {
		t.Fatalf("部署审计应用集合错误: %v", visited)
	}
	if !strings.Contains(stdout.String(), "admin") || !strings.Contains(stdout.String(), "index") {
		t.Fatalf("部署审计输出缺少应用身份: %q", stdout.String())
	}

	visited = visited[:0]
	stdout.Reset()
	input := parseMigrationCommandInput(t, command, "--app", "admin")
	if err := command.Execute(input, console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)); err != nil {
		t.Fatalf("审计显式 admin 应用失败: %v", err)
	}
	if !reflect.DeepEqual(visited, []string{"admin"}) {
		t.Fatalf("显式部署审计应用集合错误: %v", visited)
	}
}

// TestDeployCheckCommandHonorsStrictWarnings 验证普通模式允许警告、严格模式将同一警告升级为阻断。
func TestDeployCheckCommandHonorsStrictWarnings(t *testing.T) {
	app := framework.NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	stdout := &bytes.Buffer{}
	output := console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)
	command := &DeployCheck{
		Command: console.Command{App: app},
		auditor: deployAuditorWithResult(t, deploy.LevelWarn),
	}
	input := parseMigrationCommandInput(t, command)
	if err := command.Execute(input, output); err != nil {
		t.Fatalf("非严格部署检查不应阻断警告: %v", err)
	}
	if !strings.Contains(stdout.String(), "WARN") || !strings.Contains(stdout.String(), "Deployment checks passed") {
		t.Fatalf("非严格部署检查输出错误: %q", stdout.String())
	}

	stdout.Reset()
	input = parseMigrationCommandInput(t, command, "--strict")
	if err := command.Execute(input, output); !errors.Is(err, deploy.ErrDeploymentBlocked) {
		t.Fatalf("严格部署检查必须阻断警告，实际为 %v", err)
	}
}

// TestDeployCheckCommandRejectsFailedAndNilApplications 验证失败报告和空应用始终返回明确错误。
func TestDeployCheckCommandRejectsFailedAndNilApplications(t *testing.T) {
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	app := framework.NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	failed := &DeployCheck{
		Command: console.Command{App: app},
		auditor: deployAuditorWithResult(t, deploy.LevelFail),
	}
	if err := failed.Execute(parseMigrationCommandInput(t, failed), output); !errors.Is(err, deploy.ErrDeploymentBlocked) {
		t.Fatalf("失败部署检查必须返回阻断错误，实际为 %v", err)
	}
	empty := &DeployCheck{}
	if err := empty.Execute(parseMigrationCommandInput(t, empty), output); !errors.Is(err, framework.ErrNilApplication) {
		t.Fatalf("空应用必须返回 ErrNilApplication，实际为 %v", err)
	}
}

// TestDeployCheckPropagatesCancellation 验证部署审计使用控制台执行上下文，
// 宿主取消后立即停止剩余应用检查并返回可识别的 context.Canceled。
func TestDeployCheckPropagatesCancellation(t *testing.T) {
	app := framework.NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	check, err := deploy.NewCheck("cancellation", func(ctx context.Context, _ *framework.App) deploy.Result {
		if ctx.Err() != nil {
			return deploy.Result{Level: deploy.LevelFail, Message: "cancelled"}
		}
		return deploy.Result{Level: deploy.LevelPass, Message: "not cancelled"}
	})
	if err != nil {
		t.Fatalf("创建取消检查器失败: %v", err)
	}
	auditor, err := deploy.NewAuditor(check)
	if err != nil {
		t.Fatalf("创建取消审计器失败: %v", err)
	}
	command := &DeployCheck{Command: console.Command{App: app}, auditor: auditor}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	input := parseMigrationCommandInputContext(t, ctx, command)
	err = command.Execute(input, console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("部署检查必须传播 context.Canceled，实际为 %v", err)
	}
}
