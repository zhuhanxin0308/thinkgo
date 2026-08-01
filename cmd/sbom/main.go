// sbom 命令生成发布构建使用的 CycloneDX 依赖清单。
package main

import (
	"context"
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
	defaultModuleName = "thinkgo"
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

type bom struct {
	BomFormat   string      `json:"bomFormat"`
	SpecVersion string      `json:"specVersion"`
	Version     int         `json:"version"`
	Metadata    bomMetadata `json:"metadata"`
	Components  []component `json:"components"`
}

type bomMetadata struct {
	Component component `json:"component"`
}

type component struct {
	Type       string     `json:"type"`
	BomRef     string     `json:"bom-ref,omitempty"`
	Group      string     `json:"group,omitempty"`
	Name       string     `json:"name"`
	Version    string     `json:"version,omitempty"`
	PURL       string     `json:"purl,omitempty"`
	Properties []property `json:"properties,omitempty"`
}

type property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

var executeGoListCommand = executeGoList

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fail(err)
	}
}

func run(args []string, stderr io.Writer) error {
	var output string
	var version string
	var moduleName string
	flags := flag.NewFlagSet("sbom", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&output, "output", "", "CycloneDX JSON 输出路径")
	flags.StringVar(&version, "version", "", "发布版本号")
	flags.StringVar(&moduleName, "module", defaultModuleName, "应用模块名称")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(output) == "" {
		return errors.New("--output 不能为空")
	}
	if strings.TrimSpace(version) == "" {
		return errors.New("--version 不能为空")
	}
	return generate(output, moduleName, version)
}

func fail(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "生成 SBOM 失败:", err)
	os.Exit(1)
}

func generate(output, moduleName, version string) error {
	modules, err := listModules()
	if err != nil {
		return err
	}
	result, err := buildBOM(moduleName, version, modules)
	if err != nil {
		return err
	}
	return writeJSON(output, result)
}

func listModules() ([]moduleInfo, error) {
	commandContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	output, err := executeGoListCommand(commandContext)
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

func executeGoList(commandContext context.Context) ([]byte, error) {
	command := exec.CommandContext(commandContext, "go", "list", "-m", "-json", "all")
	return command.CombinedOutput()
}

func buildBOM(moduleName, version string, modules []moduleInfo) (bom, error) {
	moduleName = strings.TrimSpace(moduleName)
	version = strings.TrimSpace(version)
	if moduleName == "" || version == "" {
		return bom{}, errors.New("模块名称和版本不能为空")
	}
	components := make([]component, 0, len(modules))
	for _, module := range modules {
		current, err := moduleComponent(module)
		if err != nil {
			return bom{}, err
		}
		components = append(components, current)
	}
	sort.Slice(components, func(left, right int) bool {
		if components[left].PURL == components[right].PURL {
			return components[left].Name < components[right].Name
		}
		return components[left].PURL < components[right].PURL
	})
	return bom{
		BomFormat:   cycloneDXFormat,
		SpecVersion: cycloneDXVersion,
		Version:     1,
		Metadata: bomMetadata{Component: component{
			Type:    "application",
			Name:    moduleName,
			Version: version,
		}},
		Components: components,
	}, nil
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
