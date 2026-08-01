package framework

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
)

var (
	// ErrInvalidApplicationDefinition 表示应用定义的名称、路径或注册回调无效。
	ErrInvalidApplicationDefinition = errors.New("无效应用定义")
	// ErrDuplicateApplication 表示应用名称已经被其他定义占用。
	ErrDuplicateApplication = errors.New("应用重复定义")
)

// ApplicationDefinition 描述一个可以被宿主加载的应用。
// Path 使用相对于项目根目录的 app/<应用名> 路径，Register 只负责注册当前应用的组件。
type ApplicationDefinition struct {
	Name     string
	Path     string
	Register func(*App) error
}

var applicationDefinitions = struct {
	lock        sync.RWMutex
	definitions map[string]ApplicationDefinition
}{
	definitions: make(map[string]ApplicationDefinition),
}

// RegisterApplication 注册一个应用定义，重复名称和不安全路径会被拒绝。
func RegisterApplication(definition ApplicationDefinition) error {
	normalized, err := normalizeApplicationDefinition(definition)
	if err != nil {
		return err
	}

	applicationDefinitions.lock.Lock()
	defer applicationDefinitions.lock.Unlock()
	if _, exists := applicationDefinitions.definitions[normalized.Name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateApplication, normalized.Name)
	}
	applicationDefinitions.definitions[normalized.Name] = normalized
	return nil
}

// MustRegisterApplication 在包初始化阶段注册应用，定义错误会立即 panic。
func MustRegisterApplication(definition ApplicationDefinition) {
	if err := RegisterApplication(definition); err != nil {
		panic(err)
	}
}

// ApplicationDefinitions 返回按应用名称排序的定义快照。
// 返回值与全局注册表完全隔离，调用方可以安全修改快照内容。
func ApplicationDefinitions() []ApplicationDefinition {
	applicationDefinitions.lock.RLock()
	definitions := make([]ApplicationDefinition, 0, len(applicationDefinitions.definitions))
	for _, definition := range applicationDefinitions.definitions {
		definitions = append(definitions, definition)
	}
	applicationDefinitions.lock.RUnlock()

	sort.Slice(definitions, func(left, right int) bool {
		return definitions[left].Name < definitions[right].Name
	})
	return definitions
}

func unregisterApplicationDefinition(name string) {
	applicationDefinitions.lock.Lock()
	delete(applicationDefinitions.definitions, name)
	applicationDefinitions.lock.Unlock()
}

func normalizeApplicationDefinition(definition ApplicationDefinition) (ApplicationDefinition, error) {
	if err := validateApplicationName(definition.Name); err != nil {
		return ApplicationDefinition{}, err
	}
	if definition.Register == nil {
		return ApplicationDefinition{}, fmt.Errorf("%w: 应用 %q 的注册回调不能为空", ErrInvalidApplicationDefinition, definition.Name)
	}

	normalizedPath, err := normalizeApplicationPath(definition.Name, definition.Path)
	if err != nil {
		return ApplicationDefinition{}, err
	}
	definition.Name = strings.TrimSpace(definition.Name)
	definition.Path = normalizedPath
	return definition, nil
}

func validateApplicationName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxRegistrationNameBytes {
		return fmt.Errorf("%w: 应用名称 %q 无效", ErrInvalidApplicationDefinition, name)
	}
	for _, character := range name {
		if unicode.IsControl(character) || character == '/' || character == '\\' || character == '.' {
			return fmt.Errorf("%w: 应用名称 %q 包含非法字符", ErrInvalidApplicationDefinition, name)
		}
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '_' && character != '-' {
			return fmt.Errorf("%w: 应用名称 %q 包含非法字符", ErrInvalidApplicationDefinition, name)
		}
	}
	return nil
}

func normalizeApplicationPath(name string, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%w: 应用 %q 的路径不能为空", ErrInvalidApplicationDefinition, name)
	}
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", fmt.Errorf("%w: 应用 %q 的路径必须是项目根目录下的相对路径", ErrInvalidApplicationDefinition, name)
	}

	normalized := filepath.Clean(filepath.FromSlash(path))
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: 应用 %q 的路径不能穿越项目根目录", ErrInvalidApplicationDefinition, name)
	}
	normalizedSlash := filepath.ToSlash(normalized)
	expectedPath := filepath.ToSlash(filepath.Join("app", name))
	if normalizedSlash != expectedPath {
		return "", fmt.Errorf("%w: 应用 %q 的路径必须为 %q", ErrInvalidApplicationDefinition, name, expectedPath)
	}
	return normalizedSlash, nil
}

func safeApplicationRegister(callback func(*App) error, app *App) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", ErrRegistrationCallbackPanic, recovered)
		}
	}()
	return callback(app)
}
