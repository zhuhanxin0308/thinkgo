package command

import (
	"context"
	"fmt"
	"strings"
)

const applicationAssemblyHeader = "// 此文件由 service:discover 生成应用清单，不需要开发者手工维护。\n\n"

// ProjectApplicationAssemblySource 为源码 CLI 的临时宿主生成与 HTTP 相同的应用清单。
// 返回的源码属于入口 main 包，不把业务引用放入框架或项目公共 app 包。
func ProjectApplicationAssemblySource(ctx context.Context, basePath string) ([]byte, error) {
	if ctx == nil {
		return nil, fmt.Errorf("应用发现上下文不能为空")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	modulePath, err := readApplicationModulePath(basePath)
	if err != nil {
		return nil, err
	}
	semantic := newApplicationSemanticContextWithContext(ctx, basePath, modulePath)
	applications, err := discoverNativeApplications(semantic, "app")
	if err != nil {
		return nil, err
	}
	return buildNativeApplicationCatalogSource(modulePath, applications)
}

func buildNativeApplicationCatalogSource(modulePath string, applications []discoveredNativeApplication) ([]byte, error) {
	var source strings.Builder
	source.WriteString(applicationAssemblyHeader + "package main\n\nimport (\n")
	source.WriteString("framework \"github.com/zhuhanxin0308/thinkgo/v3\"\n")
	fmt.Fprintf(&source, "businessapp %q\n", modulePath+"/app")
	for _, application := range applications {
		fmt.Fprintf(&source, "%s %q\n", application.importAlias, modulePath+"/app/"+application.name)
	}
	source.WriteString(")\n\n// registerApplications 将当前项目的静态清单交给框架装配。\n")
	source.WriteString("func registerApplications(application *framework.App) error {\n")
	source.WriteString("return application.RegisterApplications(businessapp.RegisterGlobals,\n")
	for _, application := range applications {
		fmt.Fprintf(&source, "%s.Definition(),\n", application.importAlias)
	}
	source.WriteString(")\n}\n")
	return formatControllerDiscoverySource([]byte(source.String()))
}

func buildGlobalApplicationSource(entryPoints applicationEntryPoints) ([]byte, error) {
	var source strings.Builder
	source.WriteString(applicationAssemblyHeader + "package app\n\n")
	source.WriteString("import framework \"github.com/zhuhanxin0308/thinkgo/v3\"\n\n")
	source.WriteString("// RegisterGlobals 声明项目公共扩展，通用注册逻辑由框架执行。\n")
	source.WriteString("func RegisterGlobals(application *framework.App) error {\n")
	source.WriteString("return application.RegisterGlobalComponents(framework.GlobalApplicationComponents{\n")
	for _, field := range []struct {
		name    string
		present bool
	}{
		{"Services", entryPoints.services}, {"Events", entryPoints.events},
		{"Middleware", entryPoints.middleware}, {"Providers", entryPoints.providers},
	} {
		if field.present {
			fmt.Fprintf(&source, "%s: %s(),\n", field.name, field.name)
		}
	}
	source.WriteString("})\n}\n")
	return formatControllerDiscoverySource([]byte(source.String()))
}
