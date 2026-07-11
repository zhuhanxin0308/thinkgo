package lang

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Lang 多语言管理器
// 支持嵌套JSON结构的语言文件，使用点号分隔的key访问
type Lang struct {
	data        map[string]map[string]string // 扁平化的语言数据
	rangeL      string                       // 当前语言
	defaultLang string                       // 默认语言
	lock        sync.RWMutex
}

// NewLang 创建新的Lang管理器
func NewLang() *Lang {
	return &Lang{
		data:        make(map[string]map[string]string),
		rangeL:      "zh-cn",
		defaultLang: "zh-cn",
	}
}

// Init 初始化语言管理器
func (l *Lang) Init(config map[string]interface{}) {
	if defaultLang, ok := config["default_lang"].(string); ok {
		defaultLang = strings.ToLower(defaultLang)
		l.rangeL = defaultLang
		l.defaultLang = defaultLang
	}
}

// GetDefaultLang 获取默认语言
func (l *Lang) GetDefaultLang() string {
	l.lock.RLock()
	defer l.lock.RUnlock()
	return l.defaultLang
}

// SetLang 设置当前语言标识。
func (l *Lang) SetLang(lang string) {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.rangeL = strings.ToLower(lang)
	if _, ok := l.data[l.rangeL]; !ok {
		l.data[l.rangeL] = make(map[string]string)
	}
}

// GetLang 获取当前语言标识。
func (l *Lang) GetLang() string {
	l.lock.RLock()
	defer l.lock.RUnlock()
	return l.rangeL
}

// HasLang 检查是否支持指定语言
func (l *Lang) HasLang(lang string) bool {
	l.lock.RLock()
	defer l.lock.RUnlock()
	lang = strings.ToLower(lang)
	_, ok := l.data[lang]
	return ok
}

// FindLangByPrefix 根据语言主码查找匹配的语言包
// 例如：传入 "en" 可以匹配到 "en-us"，传入 "zh" 可以匹配到 "zh-cn"
func (l *Lang) FindLangByPrefix(prefix string) string {
	l.lock.RLock()
	defer l.lock.RUnlock()
	prefix = strings.ToLower(prefix) + "-"
	for lang := range l.data {
		if strings.HasPrefix(lang, prefix) {
			return lang
		}
	}
	return ""
}

// Load 加载语言文件
// 支持嵌套JSON结构，会自动扁平化为点号分隔的key
func (l *Lang) Load(file string, lang string) error {
	l.lock.Lock()
	defer l.lock.Unlock()

	if lang == "" {
		lang = l.rangeL
	}
	lang = strings.ToLower(lang)

	if _, ok := l.data[lang]; !ok {
		l.data[lang] = make(map[string]string)
	}

	content, err := os.ReadFile(file)
	if err != nil {
		return err
	}

	// 解析为通用interface以支持嵌套结构
	var data interface{}
	if err := json.Unmarshal(content, &data); err != nil {
		return err
	}

	// 扁平化嵌套结构
	l.flattenMap(data, "", lang)
	return nil
}

// flattenMap 递归扁平化嵌套的map结构
// 将 {"auth": {"title": "xxx"}} 转换为 {"auth.title": "xxx"}
func (l *Lang) flattenMap(data interface{}, prefix string, lang string) {
	switch v := data.(type) {
	case map[string]interface{}:
		for key, value := range v {
			newKey := key
			if prefix != "" {
				newKey = prefix + "." + key
			}
			l.flattenMap(value, newKey, lang)
		}
	case string:
		l.data[lang][strings.ToLower(prefix)] = v
	default:
		// 其他类型转为字符串
		l.data[lang][strings.ToLower(prefix)] = fmt.Sprintf("%v", v)
	}
}

// Parse 解析扁平语言文件并返回键值表。
func (l *Lang) Parse(file string) (map[string]string, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var data map[string]string
	if err := json.Unmarshal(content, &data); err != nil {
		return nil, err
	}
	return data, nil
}

// Has 判断指定翻译键是否存在。
func (l *Lang) Has(name string, lang string) bool {
	l.lock.RLock()
	defer l.lock.RUnlock()

	if lang == "" {
		lang = l.rangeL
	}
	lang = strings.ToLower(lang)
	name = strings.ToLower(name)

	if data, ok := l.data[lang]; ok {
		_, exists := data[name]
		return exists
	}
	return false
}

// Get 获取翻译文本，并替换 {:name} 形式的变量。
func (l *Lang) Get(name string, vars map[string]interface{}, lang string) string {
	l.lock.RLock()
	defer l.lock.RUnlock()

	if lang == "" {
		lang = l.rangeL
	}
	lang = strings.ToLower(lang)
	lowerName := strings.ToLower(name)

	var (
		value string
		found bool
	)
	if data, ok := l.data[lang]; ok {
		if v, ok := data[lowerName]; ok {
			value = v
			found = true
		}
	}
	if !found && lang != l.defaultLang {
		if data, ok := l.data[l.defaultLang]; ok {
			if v, ok := data[lowerName]; ok {
				value = v
				found = true
			}
		}
	}
	if !found {
		value = name
	}

	// Replace variables
	if len(vars) > 0 {
		for k, v := range vars {
			value = strings.ReplaceAll(value, "{:"+k+"}", fmt.Sprintf("%v", v))
		}
	}

	return value
}

// LoadAll 加载目录下全部 JSON 语言文件；目录不存在时视为未配置语言包。
func (l *Lang) LoadAll(dir string) error {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".json") {
			// 文件名即语言标识，例如 zh-cn.json。
			filename := filepath.Base(path)
			lang := strings.TrimSuffix(filename, filepath.Ext(filename))
			if err := l.Load(path, lang); err != nil {
				return fmt.Errorf("加载语言文件 %s 失败: %w", path, err)
			}
		}
		return nil
	})
}
