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
//
// 优先级（遵循 12-factor 约定）：真实进程环境变量 > .env 文件 > 默认值。
// 真实环境变量优先，使得部署期（容器/k8s/CI）或命令行（如 `run -p` 通过 os.Setenv
// 注入 SERVER_PORT）注入的值能够覆盖仓库里 .env 的默认值；.env 仅作为本地兜底。
// 注意：Load 仍然只写入内部存储、绝不调用 os.Setenv，避免把 .env 中的敏感值
// （如 DB_PASS）泄露到整个进程环境并被子进程继承。
func (e *Env) Get(key string, def ...string) string {
	if val, ok := e.Lookup(key); ok {
		return val
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

// Lookup 按优先级查找环境变量，并返回变量是否存在。
// 与 Get 不同，它能区分“未设置”和“显式设置为空”，供配置覆盖逻辑使用。
func (e *Env) Lookup(key string) (string, bool) {
	if val, ok := os.LookupEnv(key); ok {
		return val, true
	}
	if val, ok := e.data[key]; ok {
		return val, true
	}
	return "", false
}
