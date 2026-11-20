package gotool

import (
	"context"
	"debug/buildinfo"
	"go/version"
	"os"
	"os/exec"
	"strings"
)

// CommandContext 构造带安全工具链环境的 go 子进程命令。
// 继续通过 PATH 解析 "go" 以尊重调用方与测试夹具的 PATH 覆盖，同时通过 PrepareEnv 统一
// 关闭 GOWORK 并避免 GOTOOLCHAIN="<ver>+auto/+path" 在已激活更高工具链时触发降级下载。
func CommandContext(ctx context.Context, args ...string) *exec.Cmd {
	// #nosec G204 G702 -- 命令固定为 "go"，参数数组由框架调用方构造且不经过 shell。
	command := exec.CommandContext(ctx, "go", args...)
	command.Env = PrepareEnv(command, nil)
	return command
}

// PrepareEnv 返回适合执行 go 子进程的环境变量副本：
//  1. 强制设置 GOWORK=off，防止宿主工作区文件干扰项目或临时模块命令；
//  2. 在命令实际选中的 Go 版本不低于 GOTOOLCHAIN="<ver>+auto/+path" 声明的最低版本时，
//     将其规范化为 "auto"/"path"，避免子进程触发不必要的降级下载或在 GOPROXY=off 下失败。
func PrepareEnv(command *exec.Cmd, baseEnv []string) []string {
	if baseEnv == nil {
		baseEnv = os.Environ()
	}
	return prepareEnv(baseEnv, func() string { return goCommandVersion(command) })
}

// goCommandVersion 只信任实际选中标准 go 程序的构建信息，保留 PATH 包装器原有的工具链约束。
func goCommandVersion(command *exec.Cmd) string {
	if command == nil || command.Err != nil {
		return ""
	}
	file, err := os.Stat(command.Path)
	if err != nil || !file.Mode().IsRegular() {
		return ""
	}
	info, err := buildinfo.ReadFile(command.Path)
	if err != nil || info.Path != "cmd/go" {
		return ""
	}
	return info.GoVersion
}

func prepareEnv(baseEnv []string, goVersion func() string) []string {
	goroot, hasGOROOT := lookupEnv(baseEnv, "GOROOT")
	hasEnvGOROOT := hasGOROOT && strings.TrimSpace(goroot) != ""
	currentToolchain, hasToolchain := lookupEnv(baseEnv, "GOTOOLCHAIN")
	normalizedToolchain := normalizeGoToolchain(currentToolchain, goVersion, hasEnvGOROOT)
	replaceToolchain := normalizedToolchain != "" && (!hasToolchain || normalizedToolchain != currentToolchain)

	result := make([]string, 0, len(baseEnv)+2)
	for _, entry := range baseEnv {
		key, _, hasValue := strings.Cut(entry, "=")
		if !hasValue {
			result = append(result, entry)
			continue
		}
		if strings.EqualFold(key, "GOWORK") {
			continue
		}
		if replaceToolchain && strings.EqualFold(key, "GOTOOLCHAIN") {
			continue
		}
		result = append(result, entry)
	}
	result = append(result, "GOWORK=off")
	if replaceToolchain {
		result = append(result, "GOTOOLCHAIN="+normalizedToolchain)
	}
	return result
}

func lookupEnv(env []string, name string) (string, bool) {
	found := false
	value := ""
	// 同名环境变量按系统惯例以后出现者为准，跨平台统一不区分大小写匹配键名。
	for _, entry := range env {
		key, current, hasValue := strings.Cut(entry, "=")
		if hasValue && strings.EqualFold(key, name) {
			value = current
			found = true
		}
	}
	return value, found
}

// normalizeGoToolchain 在当前运行工具链不低于 "<ver>+auto"/"<ver>+path" 下限时收敛为 "auto"/"path"。
func normalizeGoToolchain(current string, goVersion func() string, hasEnvGOROOT bool) string {
	trimmed := strings.TrimSpace(current)
	if trimmed == "" {
		// 父进程切换工具链后显式设置了 GOROOT 且未携带 GOTOOLCHAIN 时，
		// 显式以当前 GOROOT 为基准启用 auto，防止子进程读取 go/env 中较低的 "<ver>+auto" 触发降级。
		if hasEnvGOROOT && version.IsValid(goVersion()) {
			return "auto"
		}
		return ""
	}
	if trimmed == "local" || trimmed == "auto" || trimmed == "path" {
		return trimmed
	}
	minimum, suffix, hasPlus := strings.Cut(trimmed, "+")
	if !hasPlus || (suffix != "auto" && suffix != "path") {
		return trimmed
	}
	if minimum == "local" {
		return suffix
	}
	if version.IsValid(minimum) {
		if selected := goVersion(); version.IsValid(selected) && version.Compare(selected, minimum) >= 0 {
			return suffix
		}
	}
	return trimmed
}
