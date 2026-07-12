package env

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Env 管理进程环境变量和可选的 .env 文件配置。
type Env struct {
	mu   sync.RWMutex
	data map[string]string
}

// NewEnv 创建空的环境变量管理器。
func NewEnv() *Env {
	return &Env{data: make(map[string]string)}
}

// Load 原子加载 KEY=VALUE 格式的环境文件。
// 文件不存在表示未提供可选配置；其他 IO 或语法错误会返回，且不会提交部分数据。
func (e *Env) Load(file string) (err error) {
	f, openErr := os.Open(file)
	if openErr != nil {
		if errors.Is(openErr, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("打开环境配置文件 %s 失败: %w", file, openErr)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("关闭环境配置文件 %s 失败: %w", file, closeErr)
		}
	}()

	loaded := make(map[string]string)
	scanner := bufio.NewScanner(f)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, parseErr := parseLine(line)
		if parseErr != nil {
			return fmt.Errorf("解析环境配置文件 %s 第 %d 行失败: %w", file, lineNumber, parseErr)
		}
		loaded[key] = value
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return fmt.Errorf("读取环境配置文件 %s 失败: %w", file, scanErr)
	}

	e.mu.Lock()
	if e.data == nil {
		// 保证 Env 的零值与 NewEnv 创建的实例具有相同可用性。
		e.data = make(map[string]string, len(loaded))
	}
	for key, value := range loaded {
		e.data[key] = value
	}
	e.mu.Unlock()
	return nil
}

// parseLine 解析并校验单行环境配置，值中允许包含等号。
func parseLine(line string) (string, string, error) {
	separator := strings.IndexByte(line, '=')
	if separator < 1 {
		return "", "", fmt.Errorf("必须使用 KEY=VALUE 格式")
	}

	key := strings.TrimSpace(line[:separator])
	if !envKeyPattern.MatchString(key) {
		return "", "", fmt.Errorf("环境变量名 %q 非法", key)
	}

	value := strings.TrimSpace(line[separator+1:])
	if value == "" {
		return key, "", nil
	}
	if value[0] == '\'' || value[0] == '"' {
		quote := value[0]
		if len(value) < 2 || value[len(value)-1] != quote {
			return "", "", fmt.Errorf("环境变量 %s 的引号未闭合", key)
		}
		value = value[1 : len(value)-1]
	} else if value[len(value)-1] == '\'' || value[len(value)-1] == '"' {
		return "", "", fmt.Errorf("环境变量 %s 的引号不匹配", key)
	}

	return key, value, nil
}

// Get 按“进程环境变量 > .env > 默认值”的优先级读取字符串。
func (e *Env) Get(key string, def ...string) string {
	if value, ok := e.Lookup(key); ok {
		return value
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

// GetBool 使用 strconv.ParseBool 的标准集合解析布尔值。
// 已存在但非法的值必须返回错误，不能回退到默认值掩盖配置问题。
func (e *Env) GetBool(key string, def ...bool) (bool, error) {
	value, ok := e.Lookup(key)
	if !ok {
		if len(def) > 0 {
			return def[0], nil
		}
		return false, nil
	}

	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("环境变量 %s 的布尔值 %q 非法: %w", key, value, err)
	}
	return parsed, nil
}

// Lookup 查找环境变量并区分“未设置”和“显式设置为空”。
func (e *Env) Lookup(key string) (string, bool) {
	if value, ok := os.LookupEnv(key); ok {
		return value, true
	}

	e.mu.RLock()
	value, ok := e.data[key]
	e.mu.RUnlock()
	return value, ok
}
