package main

import (
	"os"
	"path/filepath"
)

var runtimeLayoutDirectories = [...]string{"config", "app"}

// resolveRuntimeBasePath 选择运行时资源根目录，兼容开发目录和发布包外启动。
func resolveRuntimeBasePath() string {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return ""
	}
	executable, err := os.Executable()
	if err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
			executable = resolved
		}
		if selected := selectRuntimeBasePath(workingDirectory, executable); selected != workingDirectory || hasRuntimeLayout(filepath.Dir(executable)) {
			return selected
		}
	}
	return workingDirectory
}

// selectRuntimeBasePath 在可执行文件目录存在完整资源布局时优先选择它，否则回退到当前目录。
func selectRuntimeBasePath(workingDirectory, executable string) string {
	executableDirectory := filepath.Dir(executable)
	if hasRuntimeLayout(executableDirectory) {
		return executableDirectory
	}
	if hasRuntimeLayout(workingDirectory) {
		return workingDirectory
	}
	return workingDirectory
}

// hasRuntimeLayout 检查启动应用所需的配置和应用目录是否存在且确实为目录。
func hasRuntimeLayout(directory string) bool {
	if directory == "" {
		return false
	}
	for _, child := range runtimeLayoutDirectories {
		info, err := os.Stat(filepath.Join(directory, child))
		if err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}
