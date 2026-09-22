package command

import (
	"path/filepath"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

// OptimizeRoute 构建后续 URL 生成实际读取的命名路由缓存。
type OptimizeRoute struct {
	console.Command
}

func (command *OptimizeRoute) Configure() {
	command.Signature = "optimize:route"
	command.Description = "Build app route cache."
	command.AddArgument("dir", "dir name .", false)
}

func (command *OptimizeRoute) Execute(input *console.Input, output *console.Output) error {
	if command == nil || command.App == nil {
		return framework.ErrNilApplication
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	directory, err := optimizeDirectory(input)
	if err != nil {
		return err
	}
	applications, err := compiledApplicationsForOptimization(command.App, directory, "route")
	if err != nil {
		return err
	}
	for _, application := range applications {
		if err := application.LoadRoutes(); err != nil {
			return err
		}
		router, err := resolveApplicationRoute(application)
		if err != nil {
			return err
		}
		content, err := router.ExportNameCache()
		if err != nil {
			return err
		}
		if err := writeProjectFileAtomically(command.App, filepath.Join(application.GetRuntimePath(), route.NameCacheFilename), content); err != nil {
			return err
		}
	}
	output.Info("Succeed!")
	return output.Err()
}
