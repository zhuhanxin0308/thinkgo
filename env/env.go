package env

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// legacyDatabaseEnvironmentFields 保留旧版单连接 [DATABASE] 配置的兼容字段。
// 兼容层只把明确的数据库字段映射到默认 mysql 连接，不会把未知键注入配置树。
var legacyDatabaseEnvironmentFields = map[string]struct{}{
	"TYPE":                       {},
	"HOSTNAME":                   {},
	"HOSTPORT":                   {},
	"USERNAME":                   {},
	"PASSWORD":                   {},
	"DATABASE":                   {},
	"CHARSET":                    {},
	"PREFIX":                     {},
	"DEBUG":                      {},
	"AUTO_TIMESTAMP":             {},
	"CREATE_TIME_FIELD":          {},
	"UPDATE_TIME_FIELD":          {},
	"TIMESTAMP_VALUE_TYPE":       {},
	"MAX_OPEN_CONNS":             {},
	"MAX_IDLE_CONNS":             {},
	"CONN_MAX_LIFETIME_SECONDS":  {},
	"CONN_MAX_IDLE_TIME_SECONDS": {},
	"PARAMS":                     {},
}

const (
	// maxEnvFileBytes 限制单个 .env 文件的最大字节数。
	maxEnvFileBytes int64 = 1 * 1024 * 1024
	// maxEnvLineBytes 限制单行环境变量的最大字节数。
	maxEnvLineBytes = 64 * 1024
)

var (
	// ErrEnvFileTooLarge 表示 .env 文件超过大小限制。
	ErrEnvFileTooLarge = errors.New("env file is too large")
	// ErrEnvSymlinkNotAllowed 表示 .env 路径是符号链接。
	ErrEnvSymlinkNotAllowed = errors.New("env symlink is not allowed")
	// ErrEnvFileChanged 表示文件在检查与打开之间发生了替换。
	ErrEnvFileChanged = errors.New("env file changed during open")
	// ErrEnvDuplicateKey 表示 .env 中存在大小写不敏感的重复键。
	ErrEnvDuplicateKey = errors.New("duplicate env key")
	// ErrEnvLineTooLong 表示 .env 中存在超过单行限制的内容。
	ErrEnvLineTooLong = errors.New("env line is too long")
	// ErrEnvPermissionsTooOpen 表示 .env 文件向受信主体之外的用户开放权限。
	ErrEnvPermissionsTooOpen = errors.New("env file permissions are too open")
)

// Env 管理进程环境变量和可选的 .env 文件配置。
type Env struct {
	mu              sync.RWMutex
	data            map[string]string
	processSnapshot map[string]string
}

// NewEnv 创建空的环境变量管理器。
func NewEnv() *Env {
	return &Env{data: make(map[string]string)}
}

// Snapshot 返回同时冻结文件环境与当前进程环境的独立副本。
// 后续 os.Setenv、源 Env.Load 或返回 map 的修改都不会改变该副本。
func (e *Env) Snapshot() *Env {
	if e == nil {
		return &Env{data: make(map[string]string), processSnapshot: make(map[string]string)}
	}
	fileValues, processValues := e.readEnvironmentValues(currentProcessEnvironment)
	return &Env{data: fileValues, processSnapshot: processValues}
}

func (e *Env) readEnvironmentValues(readProcess func() map[string]string) (map[string]string, map[string]string) {
	e.mu.RLock()
	var processValues map[string]string
	if e.processSnapshot == nil {
		e.mu.RUnlock()
		processValues = readProcess()
		e.mu.RLock()
	}
	defer e.mu.RUnlock()
	if e.processSnapshot != nil {
		// 进程读取期间可能已经安装冻结快照，此时文件和进程值统一取新快照。
		processValues = cloneEnvironmentValues(e.processSnapshot)
	}
	return cloneEnvironmentValues(e.data), processValues
}

// ReplaceWithSnapshot 原子替换当前文件环境，并冻结为输入快照捕获的进程环境。
// 输入为 nil 时替换为空快照；调用完成后两个 Env 不共享 map。
func (e *Env) ReplaceWithSnapshot(snapshot *Env) {
	if e == nil {
		return
	}
	frozen := snapshot.Snapshot()
	e.mu.Lock()
	e.data = cloneEnvironmentValues(frozen.data)
	e.processSnapshot = cloneEnvironmentValues(frozen.processSnapshot)
	e.mu.Unlock()
}

// Load 原子加载 KEY=VALUE 格式的环境文件。
// 文件不存在表示未提供可选配置；其他 IO 或语法错误会返回，且不会提交部分数据。
func (e *Env) Load(file string) (err error) {
	info, statErr := os.Lstat(file)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("读取环境配置文件 %s 元数据失败: %w", file, statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrEnvSymlinkNotAllowed, file)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("环境配置路径不是普通文件: %s", file)
	}
	if info.Size() > maxEnvFileBytes {
		return fmt.Errorf("%w: %s", ErrEnvFileTooLarge, file)
	}

	// #nosec G304 -- Env.Load 是显式的本地配置文件 API，且已先拒绝符号链接和非普通文件。
	f, openErr := os.Open(file)
	if openErr != nil {
		return fmt.Errorf("打开环境配置文件 %s 失败: %w", file, openErr)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("关闭环境配置文件 %s 失败: %w", file, closeErr)
		}
	}()
	openedInfo, statErr := f.Stat()
	if statErr != nil {
		return fmt.Errorf("读取环境配置文件 %s 元数据失败: %w", file, statErr)
	}
	if !os.SameFile(info, openedInfo) {
		return fmt.Errorf("%w: %s", ErrEnvFileChanged, file)
	}
	if err := verifyEnvFilePermissions(f, openedInfo); err != nil {
		return fmt.Errorf("检查环境配置文件 %s 权限失败: %w", file, err)
	}

	content, readErr := io.ReadAll(io.LimitReader(f, maxEnvFileBytes+1))
	if readErr != nil {
		return fmt.Errorf("读取环境配置文件 %s 失败: %w", file, readErr)
	}
	if int64(len(content)) > maxEnvFileBytes {
		return fmt.Errorf("%w: %s", ErrEnvFileTooLarge, file)
	}

	loaded := make(map[string]string)
	loadedKeys := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, maxEnvLineBytes), maxEnvLineBytes+1)
	lineNumber := 0
	section := ""
	for scanner.Scan() {
		lineNumber++
		rawLine := scanner.Text()
		if lineNumber == 1 {
			rawLine = strings.TrimPrefix(rawLine, "\xef\xbb\xbf")
		}
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			parsedSection, parseErr := parseSection(line)
			if parseErr != nil {
				return fmt.Errorf("解析环境配置文件 %s 第 %d 行失败: %w", file, lineNumber, parseErr)
			}
			section = parsedSection
			continue
		}

		key, value, parseErr := parseLine(line)
		if parseErr != nil {
			return fmt.Errorf("解析环境配置文件 %s 第 %d 行失败: %w", file, lineNumber, parseErr)
		}
		key = normalizeEnvName(key)
		if section != "" {
			key = section + "_" + key
		}
		canonicalKey := strings.ToUpper(key)
		if previous, exists := loadedKeys[canonicalKey]; exists {
			return fmt.Errorf("%w: %s conflicts with %s", ErrEnvDuplicateKey, key, previous)
		}
		loadedKeys[canonicalKey] = key
		loaded[key] = value
		if alias, exists := legacyDatabaseEnvironmentAlias(canonicalKey); exists {
			if previous, duplicate := loadedKeys[alias]; duplicate {
				return fmt.Errorf("%w: %s conflicts with %s", ErrEnvDuplicateKey, key, previous)
			}
			loadedKeys[alias] = key
			loaded[alias] = value
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return fmt.Errorf("读取环境配置文件 %s 失败: %w", file, ErrEnvLineTooLong)
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

// parseSection 解析 ThinkPHP 风格的 [SECTION] 段落名。
func parseSection(line string) (string, error) {
	if len(line) < 3 || line[len(line)-1] != ']' {
		return "", fmt.Errorf("环境变量段落必须使用 [SECTION] 格式")
	}
	section := strings.TrimSpace(line[1 : len(line)-1])
	if !envKeyPattern.MatchString(section) {
		return "", fmt.Errorf("环境变量段落名 %q 非法", section)
	}
	return normalizeEnvName(section), nil
}

// normalizeEnvName 按 ThinkPHP 规则将点号名称转换为大写下划线名称。
func normalizeEnvName(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, ".", "_"))
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
	switch value[0] {
	case '"':
		unquoted, err := parseDoubleQuotedEnvValue(key, value)
		if err != nil {
			return "", "", err
		}
		return key, unquoted, nil
	case '\'':
		unquoted, err := parseSingleQuotedEnvValue(key, value)
		if err != nil {
			return "", "", err
		}
		return key, unquoted, nil
	default:
		if value[len(value)-1] == '\'' || value[len(value)-1] == '"' {
			return "", "", fmt.Errorf("环境变量 %s 的引号不匹配", key)
		}
		return key, value, nil
	}
}

func parseDoubleQuotedEnvValue(key, value string) (string, error) {
	var builder strings.Builder
	escaped := false
	for index := 1; index < len(value); index++ {
		ch := value[index]
		if escaped {
			switch ch {
			case '"':
				builder.WriteByte('"')
			case '\\':
				builder.WriteByte('\\')
			case 'n':
				builder.WriteByte('\n')
			case 'r':
				builder.WriteByte('\r')
			case 't':
				builder.WriteByte('\t')
			default:
				builder.WriteByte('\\')
				builder.WriteByte(ch)
			}
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			rest := strings.TrimSpace(value[index+1:])
			if rest != "" && !strings.HasPrefix(rest, "#") && !strings.HasPrefix(rest, ";") {
				return "", fmt.Errorf("环境变量 %s 的引号不匹配", key)
			}
			return builder.String(), nil
		}
		builder.WriteByte(ch)
	}
	return "", fmt.Errorf("环境变量 %s 的引号未闭合", key)
}

func parseSingleQuotedEnvValue(key, value string) (string, error) {
	for index := 1; index < len(value); index++ {
		if value[index] == '\'' {
			rest := strings.TrimSpace(value[index+1:])
			if rest != "" && !strings.HasPrefix(rest, "#") && !strings.HasPrefix(rest, ";") {
				return "", fmt.Errorf("环境变量 %s 的引号不匹配", key)
			}
			return value[1:index], nil
		}
	}
	return "", fmt.Errorf("环境变量 %s 的引号未闭合", key)
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

// FileValues 返回仅包含环境文件值的独立快照。
// 文件值可能包含凭据，输出时应按允许的键筛选。
func (e *Env) FileValues() map[string]string {
	if e == nil {
		return make(map[string]string)
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return cloneEnvironmentValues(e.data)
}

// All 返回文件层与完整进程环境的合并快照，进程变量保持与 Get 相同的最高优先级。
// 返回值可能包含凭据，输出时应按允许的键筛选。
func (e *Env) All() map[string]string {
	var result, processValues map[string]string
	if e == nil {
		result = make(map[string]string)
		processValues = currentProcessEnvironment()
	} else {
		result, processValues = e.readEnvironmentValues(currentProcessEnvironment)
	}
	for key, value := range processValues {
		result[key] = value
		if alias, exists := legacyDatabaseEnvironmentAlias(key); exists {
			if _, explicit := processValues[alias]; !explicit {
				result[alias] = value
			}
		}
	}
	return result
}

func cloneEnvironmentValues(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func currentProcessEnvironment() map[string]string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if !found || key == "" {
			continue
		}
		normalized := normalizeEnvName(key)
		// 仅筛选本次捕获的条目，不逐键重读进程环境，避免冻结跨时刻拼接的配置。
		if processEnvironmentKeyMatches(key, normalized) {
			values[normalized] = value
		}
	}
	return values
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
// 进程环境仅按规范大写键查询；文件键会归一化，Windows 保留系统大小写不敏感行为。
func (e *Env) Lookup(key string) (string, bool) {
	return e.lookup(key, os.LookupEnv)
}

func (e *Env) lookup(key string, readProcess func(string) (string, bool)) (string, bool) {
	key = normalizeEnvName(key)
	legacy, hasLegacy := legacyDatabaseEnvironmentFallback(key)
	var value string
	var found bool
	e.mu.RLock()
	if e.processSnapshot == nil {
		e.mu.RUnlock()
		value, found = readProcess(key)
		if !found && hasLegacy {
			value, found = readProcess(legacy)
		}
		e.mu.RLock()
	}
	defer e.mu.RUnlock()
	if e.processSnapshot != nil {
		value, found = e.processSnapshot[key]
		if !found && hasLegacy {
			value, found = e.processSnapshot[legacy]
		}
	}
	if found {
		return value, true
	}
	if value, ok := e.data[key]; ok {
		return value, true
	}
	value, ok := e.data[legacy]
	return value, hasLegacy && ok
}

// legacyDatabaseEnvironmentAlias 返回旧式数据库键对应的规范环境变量名。
func legacyDatabaseEnvironmentAlias(key string) (string, bool) {
	const prefix = "DATABASE_"
	if !strings.HasPrefix(key, prefix) {
		return "", false
	}
	field := strings.TrimPrefix(key, prefix)
	if _, exists := legacyDatabaseEnvironmentFields[field]; !exists {
		return "", false
	}
	return "DATABASE_CONNECTIONS_MYSQL_" + field, true
}

// legacyDatabaseEnvironmentFallback 返回规范路径对应的旧式系统环境变量名。
func legacyDatabaseEnvironmentFallback(key string) (string, bool) {
	const prefix = "DATABASE_CONNECTIONS_MYSQL_"
	if !strings.HasPrefix(key, prefix) {
		return "", false
	}
	field := strings.TrimPrefix(key, prefix)
	if _, exists := legacyDatabaseEnvironmentFields[field]; !exists {
		return "", false
	}
	return "DATABASE_" + field, true
}
