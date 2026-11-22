// Package logcoord 统一文件日志驱动与显式清理命令识别的日志及协调文件名。
package logcoord

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DirectoryGuard 在同一物理日志目录中保持稳定，自动治理和显式清理均保留它。
const DirectoryGuard = ".thinkgo-log.guard"
const ownerPrefix = ".thinkgo-log.owner-"
const ownerSuffix = ".guard"

// OwnerPath 返回与日志数据分离的持有者锁路径。
func OwnerPath(path string) string {
	return filepath.Join(filepath.Dir(path), ownerPrefix+filepath.Base(path)+ownerSuffix)
}

// OwnedLog 只识别驱动实际生成的规范侧文件名。
func OwnedLog(name string) (string, bool) {
	name, prefix := strings.CutPrefix(name, ownerPrefix)
	name, suffix := strings.CutSuffix(name, ownerSuffix)
	if !prefix || !suffix {
		return "", false
	}
	if _, index, rotated := strings.Cut(strings.TrimSuffix(name, ".log"), "_"); rotated {
		number, err := strconv.Atoi(index)
		if err != nil || strconv.Itoa(number) != index {
			return "", false
		}
	}
	_, managed := ManagedDate(name, time.UTC)
	return name, managed
}

// IsMetadata 判断显式清理必须保留的协调文件。
func IsMetadata(name string) bool {
	_, owner := OwnedLog(name)
	return name == DirectoryGuard || owner
}

// ManagedDate 沿用受管日志的日期及正整数轮转编号规则。
func ManagedDate(name string, location *time.Location) (time.Time, bool) {
	base, ok := strings.CutSuffix(name, ".log")
	if !ok || len(base) < len(time.DateOnly) {
		return time.Time{}, false
	}
	date, err := time.ParseInLocation(time.DateOnly, base[:len(time.DateOnly)], location)
	if err != nil {
		return time.Time{}, false
	}
	remainder := base[len(time.DateOnly):]
	if remainder == "" {
		return date, true
	}
	number, ok := strings.CutPrefix(remainder, "_")
	index, err := strconv.Atoi(number)
	return date, ok && err == nil && index > 0
}
