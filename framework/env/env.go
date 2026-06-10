package env

import (
	"bufio"
	"os"
	"strings"
)

// Env 环境变量管理器
// 负责加载 .env 文件并提供环境变量读取接口
type Env struct {
	data map[string]string
}

// NewEnv 创建环境变量管理器实例
func NewEnv() *Env {
	return &Env{
		data: make(map[string]string),
	}
}

// Load 从文件加载环境变量
// 支持标准 KEY=VALUE 格式，# 开头为注释，空行自动跳过。
// 安全策略：仅写入 Env 内部存储，不调用 os.Setenv，避免把 .env 中的敏感值
// （如 DB_PASS）泄露到整个进程环境并被子进程继承。
func (e *Env) Load(file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			// 移除引号包裹
			val = strings.Trim(val, `"'`)
			e.data[key] = val
		}
	}
	return scanner.Err()
}

// Get 获取环境变量值
// 优先从 .env 文件加载的数据中读取，其次读取系统环境变量
func (e *Env) Get(key string, def ...string) string {
	if val, ok := e.data[key]; ok {
		return val
	}
	val := os.Getenv(key)
	if val != "" {
		return val
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}
