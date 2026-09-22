package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

const maxVendorPublishFileBytes int64 = 8 * 1024 * 1024

// VendorPublish 按 Composer installed.json 发布供应商包配置，
// 元数据约定与 ThinkPHP vendor:publish 保持一致。
type VendorPublish struct {
	console.Command
}

// Configure 配置 vendor:publish [--force|-f] 命令。
func (command *VendorPublish) Configure() {
	command.Signature = "vendor:publish"
	command.Description = "Publish any publishable assets from vendor packages"
	command.AddBoolOption("force", "f", "Overwrite any existing files")
}

// Execute 扫描 extra.think.config 并把声明的配置发布到根 config 目录。
func (command *VendorPublish) Execute(input *console.Input, output *console.Output) error {
	if command == nil || command.App == nil {
		return framework.ErrNilApplication
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	manifestPath := filepath.Join(command.App.GetRootPath(), "vendor", "composer", "installed.json")
	manifest, exists, err := readProjectRegularFile(command.App, manifestPath, maxVendorPublishFileBytes)
	if err != nil {
		return fmt.Errorf("读取 Composer installed.json 失败: %w", err)
	}
	if !exists {
		return nil
	}
	packages, err := decodeComposerPackages(manifest)
	if err != nil {
		return fmt.Errorf("解析 Composer installed.json 失败: %w", err)
	}
	force := input != nil && input.GetOption("force") == "true"
	for _, current := range packages {
		if err := publishPackageConfig(command.App, current, force, output); err != nil {
			return err
		}
	}
	output.Info("Succeed!")
	return output.Err()
}

type composerPackage struct {
	Name  string        `json:"name"`
	Extra composerExtra `json:"extra"`
}

type composerExtra struct {
	Think composerThink `json:"think"`
}

type composerThink struct {
	Config json.RawMessage `json:"config"`
}

func decodeComposerPackages(content []byte) ([]composerPackage, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("元数据不能为空")
	}
	if trimmed[0] == '[' {
		var packages []composerPackage
		if err := json.Unmarshal(trimmed, &packages); err != nil {
			return nil, err
		}
		return packages, nil
	}
	var wrapper struct {
		Packages []composerPackage `json:"packages"`
	}
	if err := json.Unmarshal(trimmed, &wrapper); err != nil {
		return nil, err
	}
	return wrapper.Packages, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return fmt.Errorf("JSON 包含尾随数据")
}

func publishPackageConfig(app *framework.App, current composerPackage, force bool, output *console.Output) error {
	if len(current.Extra.Think.Config) == 0 || bytes.Equal(bytes.TrimSpace(current.Extra.Think.Config), []byte("null")) {
		return nil
	}
	packageName, err := safeComposerPackageName(current.Name)
	if err != nil {
		return err
	}
	configs, err := decodePublishConfigMap(current.Extra.Think.Config)
	if err != nil {
		return fmt.Errorf("供应商包 %q 的 extra.think.config 无效: %w", packageName, err)
	}
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := publishPackageConfigFile(app, packageName, name, configs[name], force, output); err != nil {
			return err
		}
	}
	return nil
}

func decodePublishConfigMap(raw json.RawMessage) (map[string]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return map[string]string{}, nil
	}
	if trimmed[0] == '"' {
		var source string
		if err := json.Unmarshal(trimmed, &source); err != nil {
			return nil, err
		}
		return map[string]string{"0": source}, nil
	}
	if trimmed[0] == '[' {
		var sources []string
		if err := json.Unmarshal(trimmed, &sources); err != nil {
			return nil, err
		}
		result := make(map[string]string, len(sources))
		for index, source := range sources {
			result[strconv.Itoa(index)] = source
		}
		return result, nil
	}
	var result map[string]string
	if err := json.Unmarshal(trimmed, &result); err != nil {
		return nil, err
	}
	if result == nil {
		return map[string]string{}, nil
	}
	return result, nil
}

func publishPackageConfigFile(app *framework.App, packageName, name, sourceName string, force bool, output *console.Output) error {
	name, err := safePublishConfigName(name)
	if err != nil {
		return fmt.Errorf("供应商包 %q 的配置名称无效: %w", packageName, err)
	}
	sourceName, err = safePackageRelativePath(sourceName)
	if err != nil {
		return fmt.Errorf("供应商包 %q 的配置来源无效: %w", packageName, err)
	}
	target := filepath.Join(app.GetConfigPath(), name+app.GetConfigExt())
	if information, statErr := os.Lstat(target); statErr == nil {
		if !force {
			output.Info(fmt.Sprintf("File %s exist!", target))
			return output.Err()
		}
		if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
			return fmt.Errorf("发布目标必须是普通文件: %s", target)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("检查发布目标失败: %w", statErr)
	}
	source := filepath.Join(app.GetRootPath(), "vendor", filepath.FromSlash(packageName), filepath.FromSlash(sourceName))
	content, exists, err := readProjectRegularFile(app, source, maxVendorPublishFileBytes)
	if err != nil {
		return fmt.Errorf("读取供应商配置 %s 失败: %w", source, err)
	}
	if !exists {
		output.Info(fmt.Sprintf("File %s not exist!", source))
		return output.Err()
	}
	if err := writeProjectFileAtomically(app, target, content); err != nil {
		return fmt.Errorf("发布供应商配置 %s 失败: %w", target, err)
	}
	return nil
}

func safeComposerPackageName(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, `\`, "/"))
	parts := strings.Split(name, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("composer 包名 %q 必须使用 vendor/package 格式", name)
	}
	for _, part := range parts {
		if !safePackageSegment(part) {
			return "", fmt.Errorf("composer 包名 %q 非法", name)
		}
	}
	return strings.Join(parts, "/"), nil
}

func safePackageSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("_.-", character) {
			continue
		}
		return false
	}
	return true
}

func safePublishConfigName(name string) (string, error) {
	if !utf8.ValidString(name) || strings.TrimSpace(name) != name || !safePackageSegment(name) {
		return "", fmt.Errorf("配置名 %q 不是安全文件名", name)
	}
	return name, nil
}

func safePackageRelativePath(path string) (string, error) {
	if !utf8.ValidString(path) || path == "" || strings.TrimSpace(path) != path || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", fmt.Errorf("来源必须是包内相对文件路径")
	}
	path = strings.ReplaceAll(path, `\`, "/")
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("来源不能离开供应商包目录")
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("来源包含非法目录段")
		}
	}
	return cleaned, nil
}

func readProjectRegularFile(app *framework.App, path string, maximum int64) ([]byte, bool, error) {
	relative, err := projectRelativePath(app.GetRootPath(), path)
	if err != nil {
		return nil, false, err
	}
	root, err := os.OpenRoot(app.GetRootPath())
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	information, err := root.Lstat(relative)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
		return nil, false, fmt.Errorf("文件必须是普通文件: %s", path)
	}
	if information.Size() < 0 || information.Size() > maximum {
		return nil, false, fmt.Errorf("文件超过 %d 字节限制: %s", maximum, path)
	}
	content, err := root.ReadFile(relative)
	if err != nil {
		return nil, false, err
	}
	if int64(len(content)) > maximum {
		return nil, false, fmt.Errorf("文件超过 %d 字节限制: %s", maximum, path)
	}
	return content, true, nil
}
