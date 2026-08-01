package lang

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

const (
	defaultLanguage          = "zh-cn"
	maxLanguageFileBytes     = 4 << 20
	maxLanguageNestingDepth  = 32
	maxLanguageEntries       = 100000
	maxLanguageKeyBytes      = 512
	maxLanguageTagBytes      = 35
	maxLanguageVariableBytes = 64
	maxAcceptLanguageBytes   = 8192
	maxAcceptLanguageEntries = 32
)

var (
	// ErrInvalidLangConfig 表示多语言配置包含未知键、错误类型或相互冲突的值。
	ErrInvalidLangConfig = errors.New("多语言配置非法")
	// ErrInvalidLanguageTag 表示语言标识不符合受支持的 BCP 47 子集。
	ErrInvalidLanguageTag = errors.New("语言标识非法")
	// ErrInvalidLanguageFile 表示语言文件不是无重复键的字符串叶子 JSON 对象。
	ErrInvalidLanguageFile = errors.New("语言文件非法")
	// ErrLanguageFileTooLarge 表示单个语言文件超过解析上限。
	ErrLanguageFileTooLarge = errors.New("语言文件超过大小上限")
	// ErrDuplicateLanguageKey 表示扁平化后出现重复翻译键。
	ErrDuplicateLanguageKey = errors.New("语言键重复")
	// ErrUnsafeLanguagePath 表示 LoadAll 遇到了逃逸语言目录的符号链接。
	ErrUnsafeLanguagePath = errors.New("语言文件路径越界")
	// ErrLanguageNotLoaded 表示请求设置了尚未加载或未允许的语言。
	ErrLanguageNotLoaded = errors.New("语言包未加载或未允许")
)

// DetectionConfig 是请求语言检测使用的只读配置快照。
type DetectionConfig struct {
	AutoDetectBrowser bool
	AllowedLanguages  []string
	DetectVariable    string
	UseCookie         bool
	CookieVariable    string
	HeaderVariable    string
}

// RequestDetectionConfig 是请求热路径使用的标量检测配置，不携带需要复制的语言列表。
type RequestDetectionConfig struct {
	AutoDetectBrowser bool
	DetectVariable    string
	UseCookie         bool
	CookieVariable    string
	HeaderVariable    string
}

type languageDetectionSnapshot struct {
	config      DetectionConfig
	defaultLang string
	loaded      map[string]struct{}
	allowed     map[string]struct{}
	prefixes    map[string][]string
	ordered     []string
}

// Lang 管理原子加载的扁平翻译表和无请求共享状态的检测配置。
type Lang struct {
	data              map[string]map[string]string
	currentLang       string
	defaultLang       string
	detection         DetectionConfig
	allowedSet        map[string]bool
	detectionSnapshot atomic.Pointer[languageDetectionSnapshot]
	lock              sync.RWMutex
}

// NewLang 创建使用安全默认检测配置的语言管理器。
func NewLang() *Lang {
	manager := &Lang{
		data:        make(map[string]map[string]string),
		currentLang: defaultLanguage,
		defaultLang: defaultLanguage,
		detection: DetectionConfig{
			AutoDetectBrowser: true,
			DetectVariable:    "lang",
			UseCookie:         true,
			CookieVariable:    "think_lang",
			HeaderVariable:    "think-lang",
		},
		allowedSet: make(map[string]bool),
	}
	manager.lock.Lock()
	manager.publishDetectionSnapshotLocked()
	manager.lock.Unlock()
	return manager
}

// publishDetectionSnapshotLocked 在持有语言状态锁时发布完整、不可变的请求检测快照。
func (l *Lang) publishDetectionSnapshotLocked() {
	config := l.detection
	config.AllowedLanguages = append([]string(nil), l.detection.AllowedLanguages...)
	loaded := make(map[string]struct{}, len(l.data))
	for language := range l.data {
		loaded[language] = struct{}{}
	}
	allowed := make(map[string]struct{}, len(l.allowedSet))
	for language, enabled := range l.allowedSet {
		if enabled {
			allowed[language] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(loaded))
	if len(config.AllowedLanguages) > 0 {
		for _, language := range config.AllowedLanguages {
			if _, exists := loaded[language]; exists {
				ordered = append(ordered, language)
			}
		}
	} else {
		for language := range loaded {
			ordered = append(ordered, language)
		}
		sort.Strings(ordered)
	}
	prefixes := make(map[string][]string, len(ordered))
	for _, language := range ordered {
		prefix := strings.SplitN(language, "-", 2)[0]
		prefixes[prefix] = append(prefixes[prefix], language)
	}
	l.detectionSnapshot.Store(&languageDetectionSnapshot{
		config:      config,
		defaultLang: l.defaultLang,
		loaded:      loaded,
		allowed:     allowed,
		prefixes:    prefixes,
		ordered:     ordered,
	})
}

func (l *Lang) loadDetectionSnapshot() *languageDetectionSnapshot {
	if l == nil {
		return nil
	}
	if snapshot := l.detectionSnapshot.Load(); snapshot != nil {
		return snapshot
	}
	return &languageDetectionSnapshot{
		config:      DetectionConfig{},
		defaultLang: "",
		loaded:      map[string]struct{}{},
		allowed:     map[string]struct{}{},
		prefixes:    map[string][]string{},
	}
}

func matchLanguageSnapshot(snapshot *languageDetectionSnapshot, candidate string) string {
	if snapshot == nil || strings.TrimSpace(candidate) == "" {
		return ""
	}
	normalized, err := normalizeLanguageTag(candidate)
	if err != nil {
		return ""
	}
	if _, loaded := snapshot.loaded[normalized]; loaded {
		if len(snapshot.allowed) == 0 {
			return normalized
		}
		if _, allowed := snapshot.allowed[normalized]; allowed {
			return normalized
		}
	}
	prefix := strings.SplitN(normalized, "-", 2)[0]
	for _, language := range snapshot.prefixes[prefix] {
		return language
	}
	return ""
}

type weightedLanguage struct {
	tag      string
	quality  float64
	position int
}

func parseAcceptLanguage(header string) []weightedLanguage {
	if len(header) == 0 || len(header) > maxAcceptLanguageBytes {
		return nil
	}
	parts := strings.Split(header, ",")
	if len(parts) > maxAcceptLanguageEntries {
		return nil
	}

	languages := make([]weightedLanguage, 0, len(parts))
	for position, part := range parts {
		segments := strings.Split(part, ";")
		tag := strings.TrimSpace(segments[0])
		if tag == "" {
			continue
		}
		quality, valid := parseLanguageQuality(segments[1:])
		if !valid || quality <= 0 {
			continue
		}
		languages = append(languages, weightedLanguage{tag: tag, quality: quality, position: position})
	}

	sort.SliceStable(languages, func(left, right int) bool {
		if languages[left].quality == languages[right].quality {
			return languages[left].position < languages[right].position
		}
		return languages[left].quality > languages[right].quality
	})
	return languages
}

func parseLanguageQuality(parameters []string) (float64, bool) {
	quality := 1.0
	found := false
	for _, parameter := range parameters {
		name, raw, exists := strings.Cut(strings.TrimSpace(parameter), "=")
		if !exists || !strings.EqualFold(strings.TrimSpace(name), "q") {
			continue
		}
		if found {
			return 0, false
		}
		found = true
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return 0, false
		}
		quality = value
	}
	return quality, true
}

// Init 严格解析检测配置，并在全部字段通过校验后一次性提交。
func (l *Lang) Init(config map[string]interface{}) error {
	if l == nil {
		return ErrInvalidLangConfig
	}
	allowedKeys := map[string]bool{
		"default_lang": true, "auto_detect_browser": true, "allow_lang_list": true,
		"detect_var": true, "use_cookie": true, "cookie_var": true, "header_var": true,
	}
	unknown := make([]string, 0)
	for key := range config {
		if !allowedKeys[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("%w: 未知键 %s", ErrInvalidLangConfig, strings.Join(unknown, ", "))
	}

	defaultLang := defaultLanguage
	if raw, exists := config["default_lang"]; exists {
		text, ok := raw.(string)
		if !ok {
			return fmt.Errorf("%w: default_lang 必须是字符串", ErrInvalidLangConfig)
		}
		var err error
		defaultLang, err = normalizeLanguageTag(text)
		if err != nil {
			return fmt.Errorf("%w: default_lang: %v", ErrInvalidLangConfig, err)
		}
	}

	detection := DetectionConfig{
		AutoDetectBrowser: true,
		DetectVariable:    "lang",
		UseCookie:         true,
		CookieVariable:    "think_lang",
		HeaderVariable:    "think-lang",
	}
	var err error
	if detection.AutoDetectBrowser, err = configBool(config, "auto_detect_browser", detection.AutoDetectBrowser); err != nil {
		return err
	}
	if detection.UseCookie, err = configBool(config, "use_cookie", detection.UseCookie); err != nil {
		return err
	}
	if detection.DetectVariable, err = configIdentifier(config, "detect_var", detection.DetectVariable, false); err != nil {
		return err
	}
	if detection.CookieVariable, err = configIdentifier(config, "cookie_var", detection.CookieVariable, false); err != nil {
		return err
	}
	if detection.HeaderVariable, err = configIdentifier(config, "header_var", detection.HeaderVariable, true); err != nil {
		return err
	}
	allowedLanguages, err := configLanguageList(config["allow_lang_list"])
	if err != nil {
		return err
	}
	if len(allowedLanguages) > 0 && !containsLanguage(allowedLanguages, defaultLang) {
		return fmt.Errorf("%w: allow_lang_list 必须包含默认语言 %q", ErrInvalidLangConfig, defaultLang)
	}
	detection.AllowedLanguages = append([]string(nil), allowedLanguages...)
	allowedSet := make(map[string]bool, len(allowedLanguages))
	for _, language := range allowedLanguages {
		allowedSet[language] = true
	}

	l.lock.Lock()
	if l.data == nil {
		l.data = make(map[string]map[string]string)
	}
	l.defaultLang = defaultLang
	l.currentLang = defaultLang
	l.detection = detection
	l.allowedSet = allowedSet
	l.publishDetectionSnapshotLocked()
	l.lock.Unlock()
	return nil
}

// DetectionConfig 返回请求中间件使用的防御性配置快照。
func (l *Lang) DetectionConfig() DetectionConfig {
	if l == nil {
		return DetectionConfig{}
	}
	snapshot := l.loadDetectionSnapshot()
	config := snapshot.config
	config.AllowedLanguages = append([]string(nil), snapshot.config.AllowedLanguages...)
	return config
}

// RequestDetectionConfig 返回请求检测所需的不可变标量配置，避免每个请求复制 AllowedLanguages。
func (l *Lang) RequestDetectionConfig() RequestDetectionConfig {
	if l == nil {
		return RequestDetectionConfig{}
	}
	snapshot := l.loadDetectionSnapshot()
	return RequestDetectionConfig{
		AutoDetectBrowser: snapshot.config.AutoDetectBrowser,
		DetectVariable:    snapshot.config.DetectVariable,
		UseCookie:         snapshot.config.UseCookie,
		CookieVariable:    snapshot.config.CookieVariable,
		HeaderVariable:    snapshot.config.HeaderVariable,
	}
}

// GetDefaultLang 获取默认语言。
func (l *Lang) GetDefaultLang() string {
	if l == nil {
		return defaultLanguage
	}
	l.lock.RLock()
	defer l.lock.RUnlock()
	return l.defaultLang
}

// SetLang 设置兼容 API 使用的全局当前语言；请求处理应继续把语言保存在 Request 中。
func (l *Lang) SetLang(language string) error {
	matched := l.MatchLanguage(language)
	if matched == "" {
		return fmt.Errorf("%w: %q", ErrLanguageNotLoaded, language)
	}
	l.lock.Lock()
	l.currentLang = matched
	l.lock.Unlock()
	return nil
}

// GetLang 获取兼容 API 的当前语言。
func (l *Lang) GetLang() string {
	if l == nil {
		return defaultLanguage
	}
	l.lock.RLock()
	defer l.lock.RUnlock()
	return l.currentLang
}

// HasLang 检查指定语言包是否已成功加载。
func (l *Lang) HasLang(language string) bool {
	normalized, err := normalizeLanguageTag(language)
	if err != nil || l == nil {
		return false
	}
	l.lock.RLock()
	defer l.lock.RUnlock()
	_, exists := l.data[normalized]
	return exists
}

// FindLangByPrefix 按允许列表或语言标识排序稳定查找同一主语言的语言包。
func (l *Lang) FindLangByPrefix(prefix string) string {
	return l.MatchLanguage(prefix)
}

// MatchLanguage 把完整或主语言候选解析为已加载且允许的稳定语言标识。
func (l *Lang) MatchLanguage(candidate string) string {
	if l == nil {
		return ""
	}
	return matchLanguageSnapshot(l.loadDetectionSnapshot(), candidate)
}

// DetectLanguage 按 query、cookie、header、Accept-Language、default 的顺序检测请求语言。
// 整个请求只读取一次不可变快照，避免同一请求混用不同版本的语言配置。
func (l *Lang) DetectLanguage(queryValue, cookieValue, headerValue, acceptLanguage string) string {
	if l == nil {
		return ""
	}
	snapshot := l.loadDetectionSnapshot()
	if selected := matchLanguageSnapshot(snapshot, queryValue); selected != "" {
		return selected
	}
	if snapshot.config.UseCookie {
		if selected := matchLanguageSnapshot(snapshot, cookieValue); selected != "" {
			return selected
		}
	}
	if selected := matchLanguageSnapshot(snapshot, headerValue); selected != "" {
		return selected
	}
	if snapshot.config.AutoDetectBrowser && strings.TrimSpace(acceptLanguage) != "" {
		for _, candidate := range parseAcceptLanguage(acceptLanguage) {
			if candidate.tag == "*" {
				if selected := matchLanguageSnapshot(snapshot, snapshot.defaultLang); selected != "" {
					return selected
				}
				continue
			}
			if selected := matchLanguageSnapshot(snapshot, candidate.tag); selected != "" {
				return selected
			}
		}
	}
	return snapshot.defaultLang
}

// Load 解析一个语言文件，并在成功后原子合并到对应语言包。
func (l *Lang) Load(file, language string) error {
	if l == nil {
		return ErrInvalidLanguageFile
	}
	if strings.TrimSpace(language) == "" {
		language = l.GetLang()
	}
	normalized, err := normalizeLanguageTag(language)
	if err != nil {
		return err
	}
	translations, err := parseLanguageFile(file)
	if err != nil {
		return err
	}
	l.lock.Lock()
	if l.data == nil {
		l.data = make(map[string]map[string]string)
	}
	merged := cloneTranslations(l.data[normalized])
	for key, value := range translations {
		merged[key] = value
	}
	l.data[normalized] = merged
	l.publishDetectionSnapshotLocked()
	l.lock.Unlock()
	return nil
}

// Parse 严格解析并扁平化一个语言文件，不修改管理器状态。
func (l *Lang) Parse(file string) (map[string]string, error) {
	return parseLanguageFile(file)
}

// Has 判断指定翻译键是否存在于目标语言包。
func (l *Lang) Has(name, language string) bool {
	if l == nil {
		return false
	}
	if strings.TrimSpace(language) == "" {
		language = l.GetLang()
	}
	normalized, err := normalizeLanguageTag(language)
	if err != nil {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(name))
	l.lock.RLock()
	defer l.lock.RUnlock()
	if !l.languageAllowedLocked(normalized) {
		return false
	}
	_, exists := l.data[normalized][key]
	return exists
}

// Get 获取翻译文本，缺失时回退默认语言，并仅格式化可信标量变量。
func (l *Lang) Get(name string, variables map[string]interface{}, language string) string {
	if l == nil {
		return name
	}
	l.lock.RLock()
	requested := l.currentLang
	if strings.TrimSpace(language) != "" {
		if normalized, err := normalizeLanguageTag(language); err == nil && l.languageAllowedLocked(normalized) {
			requested = normalized
		} else {
			requested = l.defaultLang
		}
	}
	key := strings.ToLower(strings.TrimSpace(name))
	value, found := l.data[requested][key]
	if !found && requested != l.defaultLang {
		value, found = l.data[l.defaultLang][key]
	}
	l.lock.RUnlock()
	if !found {
		value = name
	}
	if len(variables) == 0 {
		return value
	}
	keys := make([]string, 0, len(variables))
	for key := range variables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, variable := range keys {
		if !isValidLanguageVariable(variable) {
			continue
		}
		formatted, ok := formatLanguageVariable(variables[variable])
		if !ok {
			continue
		}
		value = strings.ReplaceAll(value, "{:"+variable+"}", formatted)
	}
	return value
}

// LoadAll 严格解析目录内全部 JSON 文件，并在所有文件成功后一次性提交。
func (l *Lang) LoadAll(directory string) error {
	if l == nil {
		return ErrInvalidLanguageFile
	}
	info, err := os.Stat(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s 不是目录", ErrInvalidLanguageFile, directory)
	}
	rootAbsolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbsolute)
	if err != nil {
		return err
	}
	loaded := make(map[string]map[string]string)
	err = filepath.Walk(rootReal, func(filePath string, fileInfo os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fileInfo.IsDir() || !strings.EqualFold(filepath.Ext(filePath), ".json") {
			return nil
		}
		realPath, evalErr := filepath.EvalSymlinks(filePath)
		if evalErr != nil {
			return evalErr
		}
		if !pathWithinLanguageRoot(rootReal, realPath) {
			return fmt.Errorf("%w: %s", ErrUnsafeLanguagePath, filePath)
		}
		language, tagErr := normalizeLanguageTag(strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)))
		if tagErr != nil {
			return tagErr
		}
		translations, parseErr := parseLanguageFile(realPath)
		if parseErr != nil {
			return fmt.Errorf("加载语言文件 %s 失败: %w", filePath, parseErr)
		}
		if loaded[language] == nil {
			loaded[language] = make(map[string]string)
		}
		for key, value := range translations {
			if _, duplicate := loaded[language][key]; duplicate {
				return fmt.Errorf("%w: %s:%s", ErrDuplicateLanguageKey, language, key)
			}
			loaded[language][key] = value
		}
		return nil
	})
	if err != nil {
		return err
	}
	replacement := make(map[string]map[string]string, len(loaded))
	for language, translations := range loaded {
		replacement[language] = cloneTranslations(translations)
	}
	l.lock.Lock()
	l.data = replacement
	if _, exists := replacement[l.currentLang]; !exists || !l.languageAllowedLocked(l.currentLang) {
		l.currentLang = l.defaultLang
	}
	l.publishDetectionSnapshotLocked()
	l.lock.Unlock()
	return nil
}

func parseLanguageFile(filePath string) (map[string]string, error) {
	content, err := readLanguageFile(filePath)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(content) {
		return nil, fmt.Errorf("%w: 文件不是有效 UTF-8", ErrInvalidLanguageFile)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	rootToken, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidLanguageFile, err)
	}
	rootDelimiter, ok := rootToken.(json.Delim)
	if !ok || rootDelimiter != '{' {
		return nil, fmt.Errorf("%w: 根节点必须是对象", ErrInvalidLanguageFile)
	}
	translations := make(map[string]string)
	if err = readLanguageObject(decoder, "", 0, translations); err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("%w: 只能包含一个 JSON 文档", ErrInvalidLanguageFile)
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidLanguageFile, err)
	}
	if len(translations) == 0 {
		return nil, fmt.Errorf("%w: 语言文件不能为空", ErrInvalidLanguageFile)
	}
	return translations, nil
}

func readLanguageObject(decoder *json.Decoder, prefix string, depth int, translations map[string]string) error {
	if depth > maxLanguageNestingDepth {
		return fmt.Errorf("%w: 嵌套深度超过 %d", ErrInvalidLanguageFile, maxLanguageNestingDepth)
	}
	seen := make(map[string]bool)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidLanguageFile, err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("%w: 对象键必须是字符串", ErrInvalidLanguageFile)
		}
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if normalizedKey == "" || strings.ContainsAny(normalizedKey, "\x00\r\n") {
			return fmt.Errorf("%w: 键 %q 非法", ErrInvalidLanguageFile, key)
		}
		if seen[normalizedKey] {
			return fmt.Errorf("%w: %s", ErrDuplicateLanguageKey, normalizedKey)
		}
		seen[normalizedKey] = true
		fullKey := normalizedKey
		if prefix != "" {
			fullKey = prefix + "." + normalizedKey
		}
		if len(fullKey) > maxLanguageKeyBytes {
			return fmt.Errorf("%w: 键长度超过 %d", ErrInvalidLanguageFile, maxLanguageKeyBytes)
		}
		valueToken, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidLanguageFile, err)
		}
		if delimiter, nested := valueToken.(json.Delim); nested {
			if delimiter != '{' {
				return fmt.Errorf("%w: %s 的值必须是字符串或对象", ErrInvalidLanguageFile, fullKey)
			}
			if err = readLanguageObject(decoder, fullKey, depth+1, translations); err != nil {
				return err
			}
			continue
		}
		text, ok := valueToken.(string)
		if !ok {
			return fmt.Errorf("%w: %s 的叶子值必须是字符串", ErrInvalidLanguageFile, fullKey)
		}
		if _, duplicate := translations[fullKey]; duplicate {
			return fmt.Errorf("%w: %s", ErrDuplicateLanguageKey, fullKey)
		}
		if len(translations) >= maxLanguageEntries {
			return fmt.Errorf("%w: 条目超过 %d", ErrInvalidLanguageFile, maxLanguageEntries)
		}
		translations[fullKey] = text
	}
	closingToken, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidLanguageFile, err)
	}
	if delimiter, ok := closingToken.(json.Delim); !ok || delimiter != '}' {
		return fmt.Errorf("%w: 对象未正确结束", ErrInvalidLanguageFile)
	}
	return nil
}

func readLanguageFile(filePath string) (content []byte, err error) {
	// #nosec G304 -- filePath 由应用语言目录解析器生成，加载前已完成应用根目录边界校验。
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrInvalidLanguageFile
	}
	if info.Size() < 0 || info.Size() > maxLanguageFileBytes {
		return nil, fmt.Errorf("%w: %d", ErrLanguageFileTooLarge, info.Size())
	}
	content, err = io.ReadAll(io.LimitReader(file, maxLanguageFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxLanguageFileBytes {
		return nil, ErrLanguageFileTooLarge
	}
	return content, nil
}

func (l *Lang) languageAllowedLocked(language string) bool {
	return len(l.allowedSet) == 0 || l.allowedSet[language]
}

func normalizeLanguageTag(language string) (string, error) {
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "" || len(language) > maxLanguageTagBytes || strings.Contains(language, "_") {
		return "", fmt.Errorf("%w: %q", ErrInvalidLanguageTag, language)
	}
	parts := strings.Split(language, "-")
	if len(parts) == 0 || len(parts[0]) < 2 || len(parts[0]) > 8 {
		return "", fmt.Errorf("%w: %q", ErrInvalidLanguageTag, language)
	}
	for index, part := range parts {
		if part == "" || len(part) > 8 {
			return "", fmt.Errorf("%w: %q", ErrInvalidLanguageTag, language)
		}
		for _, char := range part {
			letter := char >= 'a' && char <= 'z'
			digit := char >= '0' && char <= '9'
			if !letter && (index == 0 || !digit) {
				return "", fmt.Errorf("%w: %q", ErrInvalidLanguageTag, language)
			}
		}
	}
	return language, nil
}

func configBool(config map[string]interface{}, key string, defaultValue bool) (bool, error) {
	value, exists := config[key]
	if !exists {
		return defaultValue, nil
	}
	parsed, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%w: %s 必须是布尔值", ErrInvalidLangConfig, key)
	}
	return parsed, nil
}

func configIdentifier(config map[string]interface{}, key, defaultValue string, header bool) (string, error) {
	value, exists := config[key]
	if !exists {
		return defaultValue, nil
	}
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	if !ok || text == "" || len(text) > maxLanguageVariableBytes {
		return "", fmt.Errorf("%w: %s 必须是非空字符串", ErrInvalidLangConfig, key)
	}
	if header {
		if !isHTTPToken(text) {
			return "", fmt.Errorf("%w: %s 不是合法请求头名", ErrInvalidLangConfig, key)
		}
		return http.CanonicalHeaderKey(text), nil
	}
	if !isValidLanguageVariable(text) {
		return "", fmt.Errorf("%w: %s 名称非法", ErrInvalidLangConfig, key)
	}
	return text, nil
}

func configLanguageList(value interface{}) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items := make([]string, 0)
	switch typed := value.(type) {
	case []string:
		items = append(items, typed...)
	case []interface{}:
		for index, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%w: allow_lang_list 第 %d 项必须是字符串", ErrInvalidLangConfig, index+1)
			}
			items = append(items, text)
		}
	default:
		return nil, fmt.Errorf("%w: allow_lang_list 必须是字符串列表", ErrInvalidLangConfig)
	}
	result := make([]string, 0, len(items))
	seen := make(map[string]bool)
	for _, item := range items {
		normalized, err := normalizeLanguageTag(item)
		if err != nil {
			return nil, fmt.Errorf("%w: allow_lang_list: %v", ErrInvalidLangConfig, err)
		}
		if seen[normalized] {
			return nil, fmt.Errorf("%w: allow_lang_list 重复 %q", ErrInvalidLangConfig, normalized)
		}
		seen[normalized] = true
		result = append(result, normalized)
	}
	return result, nil
}

func containsLanguage(languages []string, target string) bool {
	for _, language := range languages {
		if language == target {
			return true
		}
	}
	return false
}

func isValidLanguageVariable(value string) bool {
	if value == "" || len(value) > maxLanguageVariableBytes {
		return false
	}
	for index, char := range value {
		letter := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
		digit := char >= '0' && char <= '9'
		if index == 0 && !letter && char != '_' {
			return false
		}
		if index > 0 && !letter && !digit && char != '_' && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func isHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		letter := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
		digit := char >= '0' && char <= '9'
		if !letter && !digit && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(char)) {
			return false
		}
	}
	return true
}

func formatLanguageVariable(value interface{}) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case bool:
		return strconv.FormatBool(typed), true
	case int:
		return strconv.FormatInt(int64(typed), 10), true
	case int8:
		return strconv.FormatInt(int64(typed), 10), true
	case int16:
		return strconv.FormatInt(int64(typed), 10), true
	case int32:
		return strconv.FormatInt(int64(typed), 10), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case uint:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint8:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint16:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint32:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint64:
		return strconv.FormatUint(typed, 10), true
	case float32:
		number := float64(typed)
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return "", false
		}
		return strconv.FormatFloat(number, 'g', -1, 32), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", false
		}
		return strconv.FormatFloat(typed, 'g', -1, 64), true
	case json.Number:
		raw := typed.String()
		if raw == "" || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) || !json.Valid([]byte(raw)) {
			return "", false
		}
		return raw, true
	default:
		return "", false
	}
}

func cloneTranslations(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func pathWithinLanguageRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
