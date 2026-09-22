// sbom 命令生成发布构建使用的 CycloneDX 依赖清单。
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultModuleName = "github.com/zhuhanxin0308/thinkgo"
	cycloneDXFormat   = "CycloneDX"
	cycloneDXVersion  = "1.5"
)

type moduleInfo struct {
	Path     string
	Version  string
	Main     bool
	Sum      string
	GoModSum string
	Replace  *moduleInfo
}

type moduleGraphEdge struct {
	Parent string
	Child  string
}

type bom struct {
	BomFormat    string       `json:"bomFormat"`
	SpecVersion  string       `json:"specVersion"`
	SerialNumber string       `json:"serialNumber"`
	Version      int          `json:"version"`
	Metadata     bomMetadata  `json:"metadata"`
	Components   []component  `json:"components"`
	Dependencies []dependency `json:"dependencies"`
}

type bomMetadata struct {
	Timestamp string    `json:"timestamp"`
	Component component `json:"component"`
}

type component struct {
	Type       string          `json:"type"`
	BomRef     string          `json:"bom-ref,omitempty"`
	Group      string          `json:"group,omitempty"`
	Name       string          `json:"name"`
	Version    string          `json:"version,omitempty"`
	PURL       string          `json:"purl,omitempty"`
	Licenses   []licenseChoice `json:"licenses,omitempty"`
	Properties []property      `json:"properties,omitempty"`
}

type licenseChoice struct {
	License license `json:"license"`
}

type license struct {
	ID string `json:"id"`
}

type dependency struct {
	Ref       string   `json:"ref"`
	DependsOn []string `json:"dependsOn"`
}

type property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

var (
	executeGoListCommand     = executeGoList
	executeGoModGraphCommand = executeGoModGraph
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fail(err)
	}
}

func run(args []string, stderr io.Writer) error {
	var output string
	var version string
	var moduleName string
	var directory string
	var componentType string
	flags := flag.NewFlagSet("sbom", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&output, "output", "", "CycloneDX JSON 输出路径")
	flags.StringVar(&version, "version", "", "发布版本号")
	flags.StringVar(&moduleName, "module", defaultModuleName, "应用模块名称")
	flags.StringVar(&directory, "directory", ".", "包含目标 go.mod 的模块目录")
	flags.StringVar(&componentType, "component-type", "application", "CycloneDX 主组件类型")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(output) == "" {
		return errors.New("--output 不能为空")
	}
	if strings.TrimSpace(version) == "" {
		return errors.New("--version 不能为空")
	}
	return generateAt(output, moduleName, version, directory, componentType)
}

func fail(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "生成 SBOM 失败:", err)
	os.Exit(1)
}

func generate(output, moduleName, version string) error {
	return generateAt(output, moduleName, version, ".", "application")
}

func generateAt(output, moduleName, version, directory, componentType string) error {
	modules, err := listModulesAt(directory)
	if err != nil {
		return err
	}
	graph, err := listModuleGraphAt(directory)
	if err != nil {
		return err
	}
	result, err := buildBOMForType(moduleName, version, componentType, modules, graph)
	if err != nil {
		return err
	}
	return writeJSON(output, result)
}

func listModules() ([]moduleInfo, error) {
	return listModulesAt(".")
}

func listModulesAt(directory string) ([]moduleInfo, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		directory = "."
	}
	information, err := os.Stat(directory)
	if err != nil || !information.IsDir() {
		return nil, fmt.Errorf("模块目录不可用: %s", directory)
	}
	commandContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	output, err := executeGoListCommand(commandContext, directory)
	if err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("执行 go list 超时: %w", commandContext.Err())
		}
		message := strings.TrimSpace(string(output))
		if message == "" {
			return nil, fmt.Errorf("执行 go list 失败: %w", err)
		}
		return nil, fmt.Errorf("执行 go list 失败: %w: %s", err, message)
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	modules := make([]moduleInfo, 0)
	for {
		var module moduleInfo
		err := decoder.Decode(&module)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("解析模块清单失败: %w", err)
		}
		if strings.TrimSpace(module.Path) == "" {
			return nil, errors.New("模块清单包含空路径")
		}
		modules = append(modules, module)
	}
	if len(modules) == 0 {
		return nil, errors.New("模块清单为空")
	}
	return modules, nil
}

func executeGoList(commandContext context.Context, directory string) ([]byte, error) {
	command := exec.CommandContext(commandContext, "go", "list", "-m", "-json", "all")
	command.Dir = directory
	command.Env = append(os.Environ(), "GOWORK=off")
	return command.CombinedOutput()
}

func listModuleGraphAt(directory string) ([]moduleGraphEdge, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		directory = "."
	}
	information, err := os.Stat(directory)
	if err != nil || !information.IsDir() {
		return nil, fmt.Errorf("模块目录不可用: %s", directory)
	}
	commandContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	output, err := executeGoModGraphCommand(commandContext, directory)
	if err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("执行 go mod graph 超时: %w", commandContext.Err())
		}
		message := strings.TrimSpace(string(output))
		if message == "" {
			return nil, fmt.Errorf("执行 go mod graph 失败: %w", err)
		}
		return nil, fmt.Errorf("执行 go mod graph 失败: %w: %s", err, message)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	edges := make([]moduleGraphEdge, 0, len(lines))
	for lineNumber, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("模块依赖图第 %d 行格式错误", lineNumber+1)
		}
		edges = append(edges, moduleGraphEdge{Parent: fields[0], Child: fields[1]})
	}
	return edges, nil
}

func executeGoModGraph(commandContext context.Context, directory string) ([]byte, error) {
	command := exec.CommandContext(commandContext, "go", "mod", "graph")
	command.Dir = directory
	command.Env = append(os.Environ(), "GOWORK=off")
	return command.CombinedOutput()
}

func buildBOM(moduleName, version string, modules []moduleInfo, graph []moduleGraphEdge) (bom, error) {
	return buildBOMForType(moduleName, version, "application", modules, graph)
}

func buildBOMForType(moduleName, version, componentType string, modules []moduleInfo, graph []moduleGraphEdge) (bom, error) {
	moduleName = strings.TrimSpace(moduleName)
	version = strings.TrimSpace(version)
	componentType = strings.ToLower(strings.TrimSpace(componentType))
	if moduleName == "" || version == "" {
		return bom{}, errors.New("模块名称和版本不能为空")
	}
	if componentType != "application" && componentType != "library" {
		return bom{}, errors.New("主组件类型必须是 application 或 library")
	}
	components := make([]component, 0, len(modules))
	componentReferences := make(map[string]string, len(modules))
	selectedVersions := make(map[string]string, len(modules))
	mainModulePath := ""
	for _, module := range modules {
		if module.Main {
			if mainModulePath != "" {
				return bom{}, errors.New("模块清单包含多个主模块")
			}
			mainModulePath = strings.TrimSpace(module.Path)
			continue
		}
		current, err := moduleComponent(module)
		if err != nil {
			return bom{}, err
		}
		components = append(components, current)
		componentReferences[strings.TrimSpace(module.Path)] = current.BomRef
		selectedVersions[strings.TrimSpace(module.Path)] = strings.TrimSpace(module.Version)
	}
	if mainModulePath == "" {
		return bom{}, errors.New("模块清单缺少主模块")
	}
	sort.Slice(components, func(left, right int) bool {
		if components[left].PURL == components[right].PURL {
			return components[left].Name < components[right].Name
		}
		return components[left].PURL < components[right].PURL
	})
	rootReference := "pkg:golang/" + moduleName + "@" + version
	dependencySets := make(map[string]map[string]struct{}, len(components)+1)
	dependencySets[rootReference] = make(map[string]struct{})
	for _, current := range components {
		dependencySets[current.BomRef] = make(map[string]struct{})
	}
	for _, edge := range graph {
		parentPath, parentVersion := splitModuleGraphVertex(edge.Parent)
		childPath, _ := splitModuleGraphVertex(edge.Child)
		childReference, childExists := componentReferences[childPath]
		if !childExists {
			continue
		}
		parentReference := ""
		if parentPath == mainModulePath && parentVersion == "" {
			parentReference = rootReference
		} else if selectedVersions[parentPath] == parentVersion {
			parentReference = componentReferences[parentPath]
		}
		if parentReference == "" || parentReference == childReference {
			continue
		}
		dependencySets[parentReference][childReference] = struct{}{}
	}
	rootDependencies := sortedDependencyReferences(dependencySets[rootReference])
	dependencies := make([]dependency, 0, len(components)+1)
	for _, current := range components {
		dependencies = append(dependencies, dependency{
			Ref:       current.BomRef,
			DependsOn: sortedDependencyReferences(dependencySets[current.BomRef]),
		})
	}
	dependencies = append([]dependency{{Ref: rootReference, DependsOn: rootDependencies}}, dependencies...)
	serialNumber, err := newBOMSerialNumber()
	if err != nil {
		return bom{}, err
	}
	return bom{
		BomFormat:    cycloneDXFormat,
		SpecVersion:  cycloneDXVersion,
		SerialNumber: serialNumber,
		Version:      1,
		Metadata: bomMetadata{Component: component{
			Type:     componentType,
			BomRef:   rootReference,
			Name:     moduleName,
			Version:  version,
			PURL:     rootReference,
			Licenses: []licenseChoice{{License: license{ID: "Apache-2.0"}}},
		}, Timestamp: time.Now().UTC().Format(time.RFC3339)},
		Components:   components,
		Dependencies: dependencies,
	}, nil
}

func splitModuleGraphVertex(vertex string) (string, string) {
	vertex = strings.TrimSpace(vertex)
	separator := strings.LastIndex(vertex, "@")
	if separator < 0 {
		return vertex, ""
	}
	return vertex[:separator], vertex[separator+1:]
}

func sortedDependencyReferences(references map[string]struct{}) []string {
	result := make([]string, 0, len(references))
	for reference := range references {
		result = append(result, reference)
	}
	sort.Strings(result)
	return result
}

func newBOMSerialNumber() (string, error) {
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return "", fmt.Errorf("生成 SBOM 序列号失败: %w", err)
	}
	identifier[6] = identifier[6]&0x0f | 0x40
	identifier[8] = identifier[8]&0x3f | 0x80
	return fmt.Sprintf("urn:uuid:%08x-%04x-%04x-%04x-%012x",
		identifier[0:4], identifier[4:6], identifier[6:8], identifier[8:10], identifier[10:16]), nil
}

func moduleComponent(module moduleInfo) (component, error) {
	path := strings.TrimSpace(module.Path)
	if path == "" {
		return component{}, errors.New("模块路径不能为空")
	}
	version := strings.TrimSpace(module.Version)
	if module.Replace != nil {
		if strings.TrimSpace(module.Replace.Path) == "" {
			return component{}, fmt.Errorf("模块 %q 的替换路径为空", path)
		}
		if strings.TrimSpace(module.Replace.Version) != "" {
			version = strings.TrimSpace(module.Replace.Version)
		}
	}
	name := path
	group := ""
	if slash := strings.LastIndex(path, "/"); slash >= 0 {
		group = path[:slash]
		name = path[slash+1:]
	}
	purl := "pkg:golang/" + path
	if version != "" {
		purl += "@" + version
	}
	properties := make([]property, 0, 3)
	if strings.TrimSpace(module.Sum) != "" {
		properties = append(properties, property{Name: "go.module.sum", Value: strings.TrimSpace(module.Sum)})
	}
	if strings.TrimSpace(module.GoModSum) != "" {
		properties = append(properties, property{Name: "go.mod.sum", Value: strings.TrimSpace(module.GoModSum)})
	}
	if module.Replace != nil {
		properties = append(properties, property{Name: "go.module.replace", Value: strings.TrimSpace(module.Replace.Path)})
	}
	return component{
		Type:       "library",
		BomRef:     purl,
		Group:      group,
		Name:       name,
		Version:    version,
		PURL:       purl,
		Properties: properties,
	}, nil
}

func writeJSON(output string, value interface{}) error {
	output = filepath.Clean(strings.TrimSpace(output))
	if output == "." || output == "" {
		return errors.New("输出路径无效")
	}
	parent := filepath.Dir(output)
	// #nosec G301 -- SBOM 是公开构建产物，输出目录沿用可分发制品的标准权限。
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	temporary, err := os.CreateTemp(parent, ".sbom-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时输出失败: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("编码 SBOM 失败: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("刷新 SBOM 失败: %w", err)
	}
	if _, err := os.Stat(output); err == nil {
		return fmt.Errorf("输出文件已存在: %s", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查输出文件失败: %w", err)
	}
	if err := os.Rename(temporaryName, output); err != nil {
		return fmt.Errorf("提交 SBOM 文件失败: %w", err)
	}
	removeTemporary = false
	return nil
}
