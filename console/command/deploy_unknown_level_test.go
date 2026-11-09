package command

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
	"github.com/zhuhanxin0308/thinkgo/v3/deploy"
)

type unknownLevelDeploymentCheck struct{}

func (unknownLevelDeploymentCheck) Name() string { return "custom.unchecked" }
func (unknownLevelDeploymentCheck) Run(context.Context, *framework.App) deploy.Result {
	return deploy.Result{Level: "UNKNOWN"}
}

// TestDeployCheckRejectsUnknownCustomLevel 验证真实命令在两种模式下均阻断非法扩展状态。
func TestDeployCheckRejectsUnknownCustomLevel(t *testing.T) {
	app := framework.NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	auditor, err := deploy.NewAuditor(unknownLevelDeploymentCheck{})
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--strict"}} {
		stdout := &bytes.Buffer{}
		command := &DeployCheck{Command: console.Command{App: app}, auditor: auditor}
		err := command.Execute(parseMigrationCommandInput(t, command, args...), console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false))
		if !errors.Is(err, deploy.ErrDeploymentBlocked) || strings.Contains(stdout.String(), "Deployment checks passed") {
			t.Fatalf("未知状态未阻断部署: 参数=%v, 错误=%v, 输出=%q", args, err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "FAIL") || !strings.Contains(stdout.String(), "custom.unchecked") {
			t.Fatalf("阻断输出缺少可定位状态: %q", stdout.String())
		}
	}
}
