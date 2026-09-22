package command

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
	"github.com/zhuhanxin0308/thinkgo/v3/deploy"
)

// DeployCheck 执行不会覆盖配置或自动修复的生产部署门禁。
type DeployCheck struct {
	console.Command
	auditor *deploy.Auditor
}

func (command *DeployCheck) Configure() {
	command.Signature = "deploy:check"
	command.Description = "Validate production deployment readiness"
	addApplicationSelectionOption(&command.Command)
	command.AddBoolOption("strict", "s", "Treat warnings as deployment blockers")
}

func (command *DeployCheck) Execute(input *console.Input, output *console.Output) error {
	if input == nil {
		return console.ErrInvalidInput
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	if command.App == nil {
		return framework.ErrNilApplication
	}
	strict := input.GetOption("strict") == "true"
	auditor := command.auditor
	if auditor == nil {
		auditor = deploy.NewDefaultAuditor()
	}
	applications, err := compiledApplicationsForCommand(command.App, input.GetOption("app"), true)
	if err != nil {
		return err
	}
	executionContext := input.Context()
	blocked := false
	for _, current := range applications {
		if err := executionContext.Err(); err != nil {
			return err
		}
		output.Info("Application: " + current.name)
		report := auditor.Run(executionContext, current.application)
		if err := executionContext.Err(); err != nil {
			return err
		}
		for _, result := range report.Results {
			output.Writeln(fmt.Sprintf("%-4s  %-26s  %s", result.Level, result.Name, result.Message))
		}
		if report.Failed(strict) {
			blocked = true
		}
	}
	if blocked {
		return fmt.Errorf("%w: 项目中至少一个应用未通过部署门禁", deploy.ErrDeploymentBlocked)
	}
	output.Writeln("Deployment checks passed.")
	return nil
}
