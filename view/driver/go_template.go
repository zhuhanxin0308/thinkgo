package driver

import (
	"bytes"
	"container/list"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
)

const (
	defaultViewSuffix   = "html"
	maxTemplateNameSize = 2048
	maxTemplateSegments = 64
	maxTemplateFileSize = 4 << 20
	maxCachedTemplates  = 1024
)

var (
	// ErrInvalidViewConfig 表示模板根目录、后缀或配置键非法。
	ErrInvalidViewConfig = errors.New("视图配置非法")
	// ErrUnsafeTemplatePath 表示模板名或真实文件路径逃逸了视图根目录。
	ErrUnsafeTemplatePath = errors.New("模板路径非法")
	// ErrTemplateNotFound 表示模板文件不存在。
	ErrTemplateNotFound = errors.New("模板文件不存在")
	// ErrTemplateNotRegular 表示模板目标不是普通文件。
	ErrTemplateNotRegular = errors.New("模板目标不是普通文件")
	// ErrTemplateTooLarge 表示单个模板超过内存解析上限。
	ErrTemplateTooLarge = errors.New("模板文件超过大小上限")
	// ErrInvalidTemplateFunction 表示函数名、函数值或函数签名不符合 html/template 约束。
	ErrInvalidTemplateFunction = errors.New("模板函数非法")
	// ErrInvalidTemplateWriter 表示模板输出目标是空接口或类型化空指针。
	ErrInvalidTemplateWriter = errors.New("模板写入器无效")
)

// GoTemplate 基于 html/template 提供并发安全、路径隔离和有界缓存的视图驱动。
type GoTemplate struct {
	rootPath    string
	suffix      string
	cacheEnable bool
	funcMap     template.FuncMap
	cache       map[string]*templateCacheEntry
	cacheOrder  *list.List
	loads       map[string]*templateLoad
	generation  uint64
	mutex       sync.RWMutex
}

type templateSnapshot struct {
	rootPath    string
	suffix      string
	cacheEnable bool
	funcMap     template.FuncMap
	generation  uint64
}

type templateLoad struct {
	done        chan struct{}
	template    *template.Template
	err         error
	generation  uint64
	invalidated bool
}

type templateCacheEntry struct {
	parsed *template.Template
	order  *list.Element
}

// NewGoTemplate 创建尚未配置根目录的 GoTemplate 驱动。
func NewGoTemplate() *GoTemplate {
	return &GoTemplate{
		suffix:      defaultViewSuffix,
		cacheEnable: true,
		funcMap:     make(template.FuncMap),
		cache:       make(map[string]*templateCacheEntry),
		cacheOrder:  list.New(),
		loads:       make(map[string]*templateLoad),
	}
}

// Config 严格解析视图配置并原子失效旧模板缓存。
func (d *GoTemplate) Config(config map[string]interface{}) error {
	if d == nil {
		return ErrInvalidViewConfig
	}
	allowed := map[string]bool{"view_path": true, "view_suffix": true, "cache": true}
	unknown := make([]string, 0)
	for key := range config {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("%w: 未知键 %s", ErrInvalidViewConfig, strings.Join(unknown, ", "))
	}

	viewPath, ok := config["view_path"].(string)
	viewPath = strings.TrimSpace(viewPath)
	if !ok || viewPath == "" || strings.ContainsAny(viewPath, "\x00\r\n") {
		return fmt.Errorf("%w: view_path 必须是非空路径", ErrInvalidViewConfig)
	}
	rootPath, err := filepath.Abs(viewPath)
	if err != nil {
		return fmt.Errorf("%w: view_path: %v", ErrInvalidViewConfig, err)
	}

	suffix := defaultViewSuffix
	if rawSuffix, exists := config["view_suffix"]; exists {
		text, valid := rawSuffix.(string)
		if !valid {
			return fmt.Errorf("%w: view_suffix 必须是字符串", ErrInvalidViewConfig)
		}
		suffix = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(text), "."))
	}
	if !isValidTemplateSuffix(suffix) {
		return fmt.Errorf("%w: view_suffix %q 非法", ErrInvalidViewConfig, suffix)
	}

	cacheEnable := true
	if rawCache, exists := config["cache"]; exists {
		value, valid := rawCache.(bool)
		if !valid {
			return fmt.Errorf("%w: cache 必须是布尔值", ErrInvalidViewConfig)
		}
		cacheEnable = value
	}

	d.mutex.Lock()
	d.rootPath = filepath.Clean(rootPath)
	d.suffix = suffix
	d.cacheEnable = cacheEnable
	d.resetCacheLocked()
	if d.loads == nil {
		d.loads = make(map[string]*templateLoad)
	}
	d.generation++
	d.mutex.Unlock()
	return nil
}

// Fetch 渲染模板并返回字符串。
func (d *GoTemplate) Fetch(templateName string, data map[string]interface{}) (string, error) {
	return d.FetchWithFuncMap(templateName, data, nil)
}

// FetchWithFuncMap 使用单次函数覆盖渲染模板，不修改共享函数表或缓存代际。
func (d *GoTemplate) FetchWithFuncMap(templateName string, data map[string]interface{}, funcMap map[string]interface{}) (string, error) {
	var buffer bytes.Buffer
	if err := d.DisplayWithFuncMap(&buffer, templateName, data, funcMap); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

// Display 渲染模板并写入指定 writer。
func (d *GoTemplate) Display(writer io.Writer, templateName string, data map[string]interface{}) error {
	return d.DisplayWithFuncMap(writer, templateName, data, nil)
}

// DisplayWithFuncMap 克隆从未执行的缓存模板，并仅在本次执行中覆盖请求函数。
func (d *GoTemplate) DisplayWithFuncMap(writer io.Writer, templateName string, data map[string]interface{}, funcMap map[string]interface{}) error {
	if isNilTemplateWriter(writer) {
		return ErrInvalidTemplateWriter
	}
	tmpl, err := d.getTemplate(templateName)
	if err != nil {
		return err
	}
	executable, err := tmpl.Clone()
	if err != nil {
		return fmt.Errorf("克隆模板执行实例失败: %w", err)
	}
	if len(funcMap) > 0 {
		validated, validateErr := validateTemplateFuncMap(funcMap)
		if validateErr != nil {
			return validateErr
		}
		executable = executable.Funcs(validated)
	}
	return executable.Execute(writer, data)
}

// Exists 检查模板是否为根目录内可读取的普通文件，并保留真实文件系统错误。
func (d *GoTemplate) Exists(templateName string) (bool, error) {
	snapshot := d.snapshot()
	candidate, err := safeTemplatePath(snapshot.rootPath, snapshot.suffix, templateName)
	if err != nil {
		return false, err
	}
	file, _, err := openTemplateFile(snapshot.rootPath, candidate)
	if err != nil {
		if errors.Is(err, ErrInvalidViewConfig) {
			return false, err
		}
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrTemplateNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, file.Close()
}

// SetFuncMap 验证并复制模板函数，成功后原子失效已按旧函数集解析的缓存。
func (d *GoTemplate) SetFuncMap(funcMap map[string]interface{}) error {
	if d == nil {
		return ErrInvalidTemplateFunction
	}
	validated, err := validateTemplateFuncMap(funcMap)
	if err != nil {
		return err
	}
	d.mutex.Lock()
	d.funcMap = validated
	d.resetCacheLocked()
	d.generation++
	d.mutex.Unlock()
	return nil
}

func (d *GoTemplate) getTemplate(templateName string) (*template.Template, error) {
	if d == nil {
		return nil, ErrInvalidViewConfig
	}
	for {
		snapshot := d.snapshot()
		candidate, err := safeTemplatePath(snapshot.rootPath, snapshot.suffix, templateName)
		if err != nil {
			return nil, err
		}
		if !snapshot.cacheEnable {
			return parseTemplate(snapshot, candidate)
		}

		d.mutex.Lock()
		if d.generation != snapshot.generation {
			d.mutex.Unlock()
			continue
		}
		if cached := d.cache[candidate]; cached != nil {
			d.touchCacheEntryLocked(cached)
			d.mutex.Unlock()
			return cached.parsed, nil
		}
		if currentLoad := d.loads[candidate]; currentLoad != nil && currentLoad.generation == snapshot.generation {
			done := currentLoad.done
			d.mutex.Unlock()
			<-done
			if currentLoad.invalidated {
				continue
			}
			return currentLoad.template, currentLoad.err
		}
		load := &templateLoad{done: make(chan struct{}), generation: snapshot.generation}
		d.loads[candidate] = load
		d.mutex.Unlock()

		tmpl, parseErr := parseTemplate(snapshot, candidate)
		d.mutex.Lock()
		if d.loads[candidate] == load {
			delete(d.loads, candidate)
		}
		if d.generation != snapshot.generation {
			load.invalidated = true
		} else {
			if parseErr == nil {
				if cached := d.cache[candidate]; cached != nil {
					d.touchCacheEntryLocked(cached)
					tmpl = cached.parsed
				} else {
					d.storeTemplateLocked(candidate, tmpl)
				}
			}
			load.template = tmpl
			load.err = parseErr
		}
		close(load.done)
		d.mutex.Unlock()
		if load.invalidated {
			continue
		}
		return load.template, load.err
	}
}

// resetCacheLocked 清空模板缓存及其最近使用顺序；调用方必须持有写锁。
func (d *GoTemplate) resetCacheLocked() {
	d.cache = make(map[string]*templateCacheEntry)
	d.cacheOrder = list.New()
}

// touchCacheEntryLocked 把命中的模板移动到最近使用端；调用方必须持有写锁。
func (d *GoTemplate) touchCacheEntryLocked(entry *templateCacheEntry) {
	d.cacheOrder.MoveToFront(entry.order)
}

// storeTemplateLocked 在容量满时淘汰最久未使用模板，再缓存本次解析结果。
// 调用方必须持有写锁。
func (d *GoTemplate) storeTemplateLocked(candidate string, tmpl *template.Template) {
	if d.cache == nil {
		d.resetCacheLocked()
	}
	if cached := d.cache[candidate]; cached != nil {
		d.touchCacheEntryLocked(cached)
		return
	}
	if len(d.cache) >= maxCachedTemplates {
		d.evictLeastRecentlyUsedLocked()
	}
	d.cache[candidate] = &templateCacheEntry{
		parsed: tmpl,
		order:  d.cacheOrder.PushFront(candidate),
	}
}

// evictLeastRecentlyUsedLocked 淘汰最久未命中的模板；调用方必须持有写锁，
// 且缓存与顺序链表由 storeTemplateLocked 保持一一对应。
func (d *GoTemplate) evictLeastRecentlyUsedLocked() {
	oldest := d.cacheOrder.Back()
	candidate := oldest.Value.(string)
	delete(d.cache, candidate)
	d.cacheOrder.Remove(oldest)
}

func parseTemplate(snapshot templateSnapshot, candidate string) (*template.Template, error) {
	content, err := readTemplateFile(snapshot.rootPath, candidate)
	if err != nil {
		return nil, err
	}
	tmpl := template.New(filepath.Base(candidate))
	if len(snapshot.funcMap) > 0 {
		tmpl = tmpl.Funcs(snapshot.funcMap)
	}
	return tmpl.Parse(string(content))
}

func (d *GoTemplate) snapshot() templateSnapshot {
	if d == nil {
		return templateSnapshot{}
	}
	d.mutex.RLock()
	defer d.mutex.RUnlock()
	funcMap := make(template.FuncMap, len(d.funcMap))
	for name, function := range d.funcMap {
		funcMap[name] = function
	}
	return templateSnapshot{
		rootPath:    d.rootPath,
		suffix:      d.suffix,
		cacheEnable: d.cacheEnable,
		funcMap:     funcMap,
		generation:  d.generation,
	}
}

func safeTemplatePath(rootPath, suffix, templateName string) (string, error) {
	if rootPath == "" {
		return "", ErrInvalidViewConfig
	}
	if hasUnsafeTemplateName(templateName) {
		return "", fmt.Errorf("%w: %q", ErrUnsafeTemplatePath, templateName)
	}
	normalized := strings.ReplaceAll(templateName, "\\", "/")
	if !strings.HasSuffix(strings.ToLower(normalized), "."+suffix) {
		normalized += "." + suffix
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+normalized), "/")
	if cleaned == "" || len(cleaned) > maxTemplateNameSize || len(strings.Split(cleaned, "/")) > maxTemplateSegments {
		return "", fmt.Errorf("%w: %q", ErrUnsafeTemplatePath, templateName)
	}
	target, err := filepath.Abs(filepath.Join(rootPath, filepath.FromSlash(cleaned)))
	if err != nil || !pathWithinTemplateRoot(rootPath, target) {
		return "", fmt.Errorf("%w: %q", ErrUnsafeTemplatePath, templateName)
	}
	return filepath.Clean(target), nil
}

func hasUnsafeTemplateName(templateName string) bool {
	if templateName == "" || len(templateName) > maxTemplateNameSize || strings.ContainsAny(templateName, "\x00\r\n") || filepath.IsAbs(templateName) {
		return true
	}
	normalized := strings.ReplaceAll(templateName, "\\", "/")
	if path.IsAbs(normalized) || strings.Contains(normalized, ":") || strings.Contains(normalized, "//") {
		return true
	}
	segments := strings.Split(normalized, "/")
	if len(segments) > maxTemplateSegments {
		return true
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func openTemplateFile(rootPath, candidate string) (*os.File, os.FileInfo, error) {
	rootReal, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: view_path %q: %w", ErrInvalidViewConfig, rootPath, err)
	}
	targetReal, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("%w: %w", ErrTemplateNotFound, err)
		}
		return nil, nil, err
	}
	if !pathWithinTemplateRoot(rootReal, targetReal) {
		return nil, nil, ErrUnsafeTemplatePath
	}
	file, err := os.Open(targetReal)
	if err != nil {
		return nil, nil, err
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, nil, statErr
	}
	verifiedReal, verifyErr := filepath.EvalSymlinks(candidate)
	if verifyErr != nil {
		_ = file.Close()
		return nil, nil, verifyErr
	}
	if !pathWithinTemplateRoot(rootReal, verifiedReal) {
		_ = file.Close()
		return nil, nil, ErrUnsafeTemplatePath
	}
	verifiedInfo, verifiedStatErr := os.Stat(verifiedReal)
	if verifiedStatErr != nil {
		_ = file.Close()
		return nil, nil, verifiedStatErr
	}
	if !os.SameFile(info, verifiedInfo) || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, ErrTemplateNotRegular
	}
	if info.Size() < 0 || info.Size() > maxTemplateFileSize {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%w: %d", ErrTemplateTooLarge, info.Size())
	}
	return file, info, nil
}

func readTemplateFile(rootPath, candidate string) (content []byte, err error) {
	file, _, err := openTemplateFile(rootPath, candidate)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	content, err = io.ReadAll(io.LimitReader(file, maxTemplateFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxTemplateFileSize {
		return nil, ErrTemplateTooLarge
	}
	return content, nil
}

func pathWithinTemplateRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func isValidTemplateSuffix(suffix string) bool {
	if suffix == "" || len(suffix) > 16 {
		return false
	}
	for _, char := range suffix {
		if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func validateTemplateFuncMap(source map[string]interface{}) (result template.FuncMap, err error) {
	result = make(template.FuncMap, len(source))
	for name, function := range source {
		value := reflect.ValueOf(function)
		if function == nil || value.Kind() != reflect.Func || value.IsNil() {
			return nil, fmt.Errorf("%w: %s", ErrInvalidTemplateFunction, name)
		}
		result[name] = function
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = nil
			err = fmt.Errorf("%w: %v", ErrInvalidTemplateFunction, recovered)
		}
	}()
	template.New("function-validation").Funcs(result)
	return result, nil
}

func isNilTemplateWriter(writer io.Writer) bool {
	if writer == nil {
		return true
	}
	value := reflect.ValueOf(writer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
