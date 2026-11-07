package thinkgo

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters"
)

//go:embed project/config/*.json
var projectFiles embed.FS

// createProject 将正式应用配置展开到独立目录，不写入仓库或修改框架源码。
func createProject(service adapters.Service, address string) (directory string, result error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", err
	}
	directory, err = os.MkdirTemp("", "thinkgo-business-")
	if err != nil {
		return "", err
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, os.RemoveAll(directory))
		}
	}()
	if err := os.MkdirAll(filepath.Join(directory, "app", "index"), 0o700); err != nil {
		return directory, err
	}
	if err := os.MkdirAll(filepath.Join(directory, "config"), 0o700); err != nil {
		return directory, err
	}
	entries, err := fs.ReadDir(projectFiles, "project/config")
	if err != nil {
		return directory, err
	}
	for _, entry := range entries {
		data, err := projectFiles.ReadFile("project/config/" + entry.Name())
		if err != nil {
			return directory, err
		}
		if entry.Name() == "app.json" {
			var configuration map[string]any
			if err := json.Unmarshal(data, &configuration); err != nil {
				return directory, err
			}
			server := configuration["server"].(map[string]any)
			server["host"] = host
			server["port"] = port
			for _, field := range []string{"read_header_timeout_ms", "read_timeout_ms", "write_timeout_ms", "idle_timeout_ms", "shutdown_timeout_ms"} {
				server[field] = adapters.Timeout(service).Milliseconds()
			}
			server["max_body_bytes"] = service.Limits().MaximumBodyBytes
			data, err = json.Marshal(configuration)
			if err != nil {
				return directory, err
			}
		}
		if err := os.WriteFile(filepath.Join(directory, "config", entry.Name()), data, 0o600); err != nil {
			return directory, err
		}
	}
	return directory, nil
}
