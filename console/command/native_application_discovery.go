package command

import (
	"errors"
	"fmt"
	"go/format"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/zhuhanxin0308/thinkgo/framework"
)

type generatedApplicationSource struct {
	relativePath string
	source       []byte
}

var formatControllerDiscoverySource = format.Source

type applicationEntryPoints struct {
	services   bool
	events     bool
	middleware bool
	providers  bool
}

type discoveredNativeApplication struct {
	name                  string
	packageName           string
	relativeDirectory     string
	importAlias           string
	controllerTypes       []string
	modelTypes            []string
	validatorTypes        []string
	validatorConstructors map[string]bool
	hasRoutes             bool
	entryPoints           applicationEntryPoints
	nestedComponents      []discoveredComponentPackage
}

// nativeApplicationDiscoverySources 先完整解析所有应用，再生成各应用入口和
// 最后的根清单，避免扫描失败时写出只完成一半的装配关系。
func nativeApplicationDiscoverySources(app *framework.App) ([]generatedApplicationSource, error) {
	if app == nil {
		return nil, framework.ErrNilApplication
	}
	applicationDirectory := "app"
	modulePath, err := readApplicationModulePath(app.BasePath)
	if err != nil {
		return nil, err
	}
	semanticContext := newApplicationSemanticContext(app.BasePath, modulePath)
	rootEntryPoints, err := discoverApplicationEntryPointsWithContext(semanticContext, applicationDirectory, "app")
	if err != nil {
		return nil, err
	}
	applications, err := discoverNativeApplications(semanticContext, applicationDirectory)
	if err != nil {
		return nil, err
	}
	if len(applications) == 0 {
		return nil, framework.ErrNoApplications
	}

	sources := make([]generatedApplicationSource, 0, len(applications)+1)
	for _, application := range applications {
		source, buildErr := buildNativeApplicationSource(modulePath, application)
		if buildErr != nil {
			return nil, buildErr
		}
		sources = append(sources, generatedApplicationSource{
			relativePath: filepath.Join(application.relativeDirectory, applicationDiscoveryFilename),
			source:       source,
		})
	}
	rootSource, err := buildNativeApplicationCatalogSource(modulePath, applicationDirectory, rootEntryPoints, applications)
	if err != nil {
		return nil, err
	}
	sources = append(sources, generatedApplicationSource{
		relativePath: filepath.Join(applicationDirectory, applicationDiscoveryFilename),
		source:       rootSource,
	})
	comments, err := existingOpenAPICommentSources(app.BasePath)
	if err != nil {
		return nil, err
	}
	sources = append(sources, comments...)
	return sources, nil
}

func discoverNativeApplications(semanticContext *applicationSemanticContext, applicationDirectory string) ([]discoveredNativeApplication, error) {
	root, err := os.OpenRoot(semanticContext.basePath)
	if err != nil {
		return nil, fmt.Errorf("打开项目根目录失败: %w", err)
	}
	defer root.Close()
	directory, err := root.Open(applicationDirectory)
	if err != nil {
		return nil, fmt.Errorf("打开应用目录失败: %w", err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(fmt.Errorf("读取应用目录失败: %w", readErr), closeErr)
	}

	applications := make([]discoveredNativeApplication, 0)
	aliases := make(map[string]string)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("应用目录 %q 不能是符号链接", name)
		}
		hasSource, err := nativeApplicationDirectoryHasSource(root, filepath.Join(applicationDirectory, name))
		if err != nil {
			return nil, err
		}
		if !hasSource {
			continue
		}
		if err := validateNativeApplicationPackageName(name); err != nil {
			return nil, err
		}
		alias := nativeApplicationImportAlias(name)
		if previous, exists := aliases[alias]; exists {
			return nil, fmt.Errorf("应用 %q 与 %q 生成相同导入别名 %q", previous, name, alias)
		}
		aliases[alias] = name
		application, err := inspectNativeApplication(semanticContext, applicationDirectory, name, alias)
		if err != nil {
			return nil, err
		}
		applications = append(applications, application)
	}
	sort.Slice(applications, func(left, right int) bool {
		return applications[left].name < applications[right].name
	})
	return applications, nil
}

// nativeApplicationDirectoryHasSource 忽略迁移或版本控制留下的空目录；
// 只要应用根包或标准业务层中存在 Go 源码，就把它视为原生应用。
func nativeApplicationDirectoryHasSource(root *os.Root, relativeDirectory string) (bool, error) {
	directories := []string{
		relativeDirectory,
		filepath.Join(relativeDirectory, "controller"),
		filepath.Join(relativeDirectory, "model"),
		filepath.Join(relativeDirectory, "route"),
		filepath.Join(relativeDirectory, "validate"),
	}
	for _, directoryPath := range directories {
		directory, err := root.Open(directoryPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("打开应用源码目录 %q 失败: %w", directoryPath, err)
		}
		entries, readErr := directory.ReadDir(-1)
		closeErr := directory.Close()
		if readErr != nil || closeErr != nil {
			return false, errors.Join(fmt.Errorf("读取应用源码目录 %q 失败: %w", directoryPath, readErr), closeErr)
		}
		for _, entry := range entries {
			if !entry.IsDir() && filepath.Ext(entry.Name()) == ".go" && !strings.HasSuffix(entry.Name(), "_test.go") {
				return true, nil
			}
		}
		if directoryPath != relativeDirectory {
			found, err := nestedApplicationDirectoryHasSource(root, directoryPath)
			if err != nil || found {
				return found, err
			}
		}
	}
	return false, nil
}

func inspectNativeApplication(semanticContext *applicationSemanticContext, applicationDirectory, name, alias string) (discoveredNativeApplication, error) {
	relativeDirectory := filepath.Join(applicationDirectory, name)
	controllers, _, err := discoverApplicationStructTypesWithContext(semanticContext, filepath.Join(relativeDirectory, "controller"), "controller")
	if err != nil {
		return discoveredNativeApplication{}, err
	}
	controllers = excludeApplicationType(controllers, "BaseController")
	models, _, err := discoverApplicationStructTypesWithContext(semanticContext, filepath.Join(relativeDirectory, "model"), "model")
	if err != nil {
		return discoveredNativeApplication{}, err
	}
	validators, _, err := discoverApplicationStructTypesWithContext(semanticContext, filepath.Join(relativeDirectory, "validate"), "validate")
	if err != nil {
		return discoveredNativeApplication{}, err
	}
	constructors, err := discoverApplicationConstructorsWithContext(semanticContext, filepath.Join(relativeDirectory, "validate"), "validate", validators)
	if err != nil {
		return discoveredNativeApplication{}, err
	}
	hasRoutes, err := applicationPackageExistsWithContext(semanticContext, filepath.Join(relativeDirectory, "route"), "route")
	if err != nil {
		return discoveredNativeApplication{}, err
	}
	entryPoints, err := discoverApplicationEntryPointsWithContext(semanticContext, relativeDirectory, name)
	if err != nil {
		return discoveredNativeApplication{}, err
	}
	nestedComponents, err := discoverNestedComponents(semanticContext, relativeDirectory)
	if err != nil {
		return discoveredNativeApplication{}, err
	}
	return discoveredNativeApplication{
		name:                  name,
		packageName:           name,
		relativeDirectory:     relativeDirectory,
		importAlias:           alias,
		controllerTypes:       controllers,
		modelTypes:            models,
		validatorTypes:        validators,
		validatorConstructors: constructors,
		hasRoutes:             hasRoutes,
		entryPoints:           entryPoints,
		nestedComponents:      nestedComponents,
	}, nil
}

func validateNativeApplicationPackageName(name string) error {
	if name == "" || token.Lookup(name).IsKeyword() {
		return fmt.Errorf("原生应用目录 %q 不是合法 Go 包名", name)
	}
	for index, character := range name {
		if index == 0 {
			if !unicode.IsLetter(character) && character != '_' {
				return fmt.Errorf("原生应用目录 %q 必须以字母或下划线开头", name)
			}
			continue
		}
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '_' {
			return fmt.Errorf("原生应用目录 %q 包含非法 Go 包名字符", name)
		}
	}
	return nil
}

func nativeApplicationImportAlias(name string) string {
	var alias strings.Builder
	alias.WriteString("application")
	upperNext := true
	for _, character := range name {
		if character == '_' {
			upperNext = true
			continue
		}
		if upperNext {
			alias.WriteRune(unicode.ToUpper(character))
			upperNext = false
			continue
		}
		alias.WriteRune(character)
	}
	return alias.String()
}

func discoverApplicationEntryPoints(basePath, directory, packageName string) (applicationEntryPoints, error) {
	semanticContext, err := newApplicationSemanticContextFromModule(basePath)
	if err != nil {
		return applicationEntryPoints{}, err
	}
	return discoverApplicationEntryPointsWithContext(semanticContext, directory, packageName)
}

func buildNativeApplicationCatalogSource(modulePath, applicationDirectory string, entryPoints applicationEntryPoints, applications []discoveredNativeApplication) ([]byte, error) {
	applicationImport := strings.TrimSuffix(modulePath, "/") + "/" + filepath.ToSlash(applicationDirectory)
	var source strings.Builder
	source.WriteString("// 此文件由 service:discover 根据原生多应用目录生成，不需要开发者手工维护。\n\n")
	source.WriteString("package app\n\n")
	source.WriteString("import (\n")
	if entryPoints.services || entryPoints.events || entryPoints.middleware || entryPoints.providers {
		source.WriteString("\t\"fmt\"\n")
	}
	if entryPoints.providers {
		source.WriteString("\t\"sort\"\n")
	}
	if entryPoints.services || entryPoints.events || entryPoints.middleware || entryPoints.providers {
		source.WriteString("\n")
	}
	source.WriteString("\tframework \"github.com/zhuhanxin0308/thinkgo/framework\"\n")
	for _, application := range applications {
		source.WriteString(fmt.Sprintf("\t%s %q\n", application.importAlias, applicationImport+"/"+application.name))
	}
	source.WriteString(")\n\n")
	source.WriteString("// Register 注册项目级定义和全部业务应用，HTTP 内核据此原生解析多应用。\n")
	source.WriteString("func Register(application *framework.App) error {\n")
	source.WriteString("\tif application == nil {\n\t\treturn framework.ErrNilApplication\n\t}\n")
	source.WriteString("\treturn application.RegisterApplications(registerGlobalApplication,\n")
	for _, application := range applications {
		source.WriteString(fmt.Sprintf("\t\t%s.Definition(),\n", application.importAlias))
	}
	source.WriteString("\t)\n}\n\n")
	source.WriteString("// registerGlobalApplication 装配根 app 下的全局事件、服务、中间件和容器绑定。\n")
	source.WriteString("func registerGlobalApplication(current *framework.App) error {\n")
	writeGlobalApplicationRegistrations(&source, entryPoints)
	source.WriteString("\treturn nil\n}\n")
	formatted, err := formatControllerDiscoverySource([]byte(source.String()))
	if err != nil {
		return nil, fmt.Errorf("格式化根应用发现源码失败: %w", err)
	}
	return formatted, nil
}

func writeGlobalApplicationRegistrations(source *strings.Builder, entryPoints applicationEntryPoints) {
	if entryPoints.services {
		source.WriteString("\tfor _, service := range Services() {\n\t\tif err := current.Register(service); err != nil {\n\t\t\treturn fmt.Errorf(\"注册全局应用服务失败: %w\", err)\n\t\t}\n\t}\n")
	}
	if entryPoints.events {
		source.WriteString("\tif err := current.LoadEvent(Events()); err != nil {\n\t\treturn fmt.Errorf(\"注册全局应用事件失败: %w\", err)\n\t}\n")
	}
	if entryPoints.middleware {
		source.WriteString("\tfor _, handler := range Middleware() {\n\t\tif err := current.RegisterGlobalMiddleware(handler); err != nil {\n\t\t\treturn fmt.Errorf(\"注册全局应用中间件失败: %w\", err)\n\t\t}\n\t}\n")
	}
	if entryPoints.providers {
		writeProviderRegistrations(source, "current", "全局应用")
	}
}

func buildNativeApplicationSource(modulePath string, application discoveredNativeApplication) ([]byte, error) {
	applicationImport := strings.TrimSuffix(modulePath, "/") + "/" + filepath.ToSlash(application.relativeDirectory)
	var source strings.Builder
	source.WriteString("// 此文件由 service:discover 根据当前业务应用生成，不需要开发者手工维护。\n\n")
	source.WriteString("package " + application.packageName + "\n\n")
	source.WriteString("import (\n")
	source.WriteString("\tframework \"github.com/zhuhanxin0308/thinkgo/framework\"\n")
	if len(application.controllerTypes) > 0 {
		source.WriteString(fmt.Sprintf("\tapplicationController %q\n", applicationImport+"/controller"))
	}
	if len(application.modelTypes) > 0 {
		source.WriteString(fmt.Sprintf("\tapplicationModel %q\n", applicationImport+"/model"))
	}
	if len(application.validatorTypes) > 0 {
		source.WriteString(fmt.Sprintf("\tapplicationValidate %q\n", applicationImport+"/validate"))
	}
	if application.hasRoutes {
		source.WriteString(fmt.Sprintf("\tapplicationRoute %q\n", applicationImport+"/route"))
	}
	for _, component := range application.nestedComponents {
		source.WriteString(fmt.Sprintf("\t%s %q\n", component.alias, strings.TrimSuffix(modulePath, "/")+"/"+filepath.ToSlash(component.directory)))
	}
	source.WriteString(")\n\n")
	source.WriteString("// Definition 返回当前业务应用的编译期定义。\n")
	source.WriteString("func Definition() framework.ApplicationDefinition {\n")
	source.WriteString(fmt.Sprintf("\treturn framework.ApplicationDefinition{Name: %q, Register: registerApplication}\n", application.name))
	source.WriteString("}\n\n")
	source.WriteString("func registerApplication(current *framework.App) error {\n")
	writeNativeApplicationRegistrations(&source, application)
	source.WriteString("}\n")
	formatted, err := formatControllerDiscoverySource([]byte(source.String()))
	if err != nil {
		return nil, fmt.Errorf("格式化应用 %q 发现源码失败: %w", application.name, err)
	}
	return formatted, nil
}

func writeNativeApplicationRegistrations(source *strings.Builder, application discoveredNativeApplication) {
	source.WriteString("\treturn current.RegisterApplicationComponents(framework.ApplicationComponents{\n")
	if application.entryPoints.services {
		source.WriteString("\t\tServices: Services(),\n")
	}
	if application.entryPoints.events {
		source.WriteString("\t\tEvents: Events(),\n")
	}
	if application.entryPoints.middleware {
		source.WriteString("\t\tMiddleware: Middleware(),\n")
	}
	if application.entryPoints.providers {
		source.WriteString("\t\tProviders: Providers(),\n")
	}
	if len(application.controllerTypes) > 0 || application.hasNestedLayer("controller") {
		source.WriteString("\t\tControllers: map[string]interface{}{\n")
		for _, name := range application.controllerTypes {
			source.WriteString(fmt.Sprintf("\t\t\t%q: &applicationController.%s{},\n", name, name))
		}
		writeNestedComponentRegistrations(source, application.nestedComponents, "controller")
		source.WriteString("\t\t},\n")
	}
	if len(application.modelTypes) > 0 || application.hasNestedLayer("model") {
		source.WriteString("\t\tModels: map[string]interface{}{\n")
		for _, name := range application.modelTypes {
			source.WriteString(fmt.Sprintf("\t\t\t%q: &applicationModel.%s{},\n", name, name))
		}
		writeNestedComponentRegistrations(source, application.nestedComponents, "model")
		source.WriteString("\t\t},\n")
	}
	if len(application.validatorTypes) > 0 || application.hasNestedLayer("validate") {
		source.WriteString("\t\tValidatorFactories: map[string]interface{}{\n")
		for _, name := range application.validatorTypes {
			factory := fmt.Sprintf("&applicationValidate.%s{}", name)
			if application.validatorConstructors[name] {
				factory = fmt.Sprintf("applicationValidate.New%s()", name)
			}
			source.WriteString(fmt.Sprintf("\t\t\t%q: func() interface{} { return %s },\n", name, factory))
		}
		writeNestedComponentRegistrations(source, application.nestedComponents, "validate")
		source.WriteString("\t\t},\n")
	}
	if application.hasRoutes {
		source.WriteString("\t\tRouteLoader: func(routeApplication *framework.App) error {\n\t\t\tapplicationRoute.Load(routeApplication.Route())\n\t\t\treturn nil\n\t\t},\n")
	}
	source.WriteString("\t})\n")
}

func writeProviderRegistrations(source *strings.Builder, applicationVariable, scope string) {
	source.WriteString("\tbindings := Providers()\n\tbindingNames := make([]string, 0, len(bindings))\n\tfor name := range bindings {\n\t\tbindingNames = append(bindingNames, name)\n\t}\n\tsort.Strings(bindingNames)\n\tfor _, name := range bindingNames {\n\t\tvar err error\n\t\tif name == string(framework.ServiceRequest) {\n")
	source.WriteString(fmt.Sprintf("\t\t\terr = %s.BindFactory(name, bindings[name])\n", applicationVariable))
	source.WriteString("\t\t} else {\n")
	source.WriteString(fmt.Sprintf("\t\t\terr = %s.Bind(name, bindings[name])\n", applicationVariable))
	source.WriteString("\t\t}\n\t\tif err != nil {\n")
	source.WriteString(fmt.Sprintf("\t\t\treturn fmt.Errorf(\"注册%s容器绑定 %%q 失败: %%w\", name, err)\n", scope))
	source.WriteString("\t\t}\n\t}\n")
}
