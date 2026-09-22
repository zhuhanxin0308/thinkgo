package command

import (
	"fmt"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/framework"
)

type compiledCommandApplication struct {
	name        string
	application *framework.App
}

// compiledApplicationsForCommand 从编译期应用清单选择控制台执行上下文。
// allWhenUnspecified 仅供部署审计等项目级命令使用；显式 --app 始终只选一个应用。
func compiledApplicationsForCommand(project *framework.App, requested string, allWhenUnspecified bool) ([]compiledCommandApplication, error) {
	if project == nil {
		return nil, framework.ErrNilApplication
	}
	requested = strings.TrimSpace(requested)
	names := project.ApplicationNames()
	if len(names) == 0 {
		name := strings.TrimSpace(project.CurrentApplicationName())
		if name == "" {
			name = "index"
		}
		if requested != "" && requested != name {
			return nil, fmt.Errorf("应用 %q 未在编译期清单中注册", requested)
		}
		return []compiledCommandApplication{{name: name, application: project}}, nil
	}

	applications, err := project.BuildApplications()
	if err != nil {
		return nil, fmt.Errorf("构建编译期应用清单失败: %w", err)
	}
	selectedNames := names
	if requested != "" {
		selectedNames = []string{requested}
	} else if !allWhenUnspecified {
		defaultName, err := commandDefaultApplication(project, names)
		if err != nil {
			return nil, err
		}
		selectedNames = []string{defaultName}
	}

	selected := make([]compiledCommandApplication, 0, len(selectedNames))
	for _, name := range selectedNames {
		application := applications[name]
		if application == nil {
			return nil, fmt.Errorf("应用 %q 未在编译期清单中注册", name)
		}
		if !application.Initialized() {
			if err := application.Initialize(); err != nil {
				return nil, fmt.Errorf("初始化应用 %q 失败: %w", name, err)
			}
		}
		if err := application.BootProviders(); err != nil {
			return nil, fmt.Errorf("启动应用 %q 服务失败: %w", name, err)
		}
		selected = append(selected, compiledCommandApplication{name: name, application: application})
	}
	return selected, nil
}

func commandDefaultApplication(project *framework.App, names []string) (string, error) {
	available := make(map[string]struct{}, len(names))
	for _, name := range names {
		available[name] = struct{}{}
	}
	defaultName := names[0]
	if _, exists := available["index"]; exists {
		defaultName = "index"
	}
	configuration := project.ProjectApplicationConfig()
	if raw, exists := configuration["default_app"]; exists {
		configured, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("app.default_app 必须是字符串")
		}
		if configured = strings.TrimSpace(configured); configured != "" {
			defaultName = configured
		}
	}
	if _, exists := available[defaultName]; !exists {
		return "", fmt.Errorf("默认应用 %q 未在编译期清单中注册", defaultName)
	}
	return defaultName, nil
}
