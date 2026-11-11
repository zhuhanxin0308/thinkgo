package env

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var compareEnvironmentKeyOrdinal = windows.NewLazySystemDLL("kernel32.dll").NewProc("CompareStringOrdinal")

// processEnvironmentKeyMatches 使用系统大小写表比较捕获的键，不再次读取进程环境。
func processEnvironmentKeyMatches(key, normalized string) bool {
	if key == normalized {
		return true
	}
	left, err := windows.UTF16PtrFromString(key)
	if err != nil {
		return false
	}
	right, err := windows.UTF16PtrFromString(normalized)
	if err != nil {
		return false
	}
	const ordinalEqual = 2
	const ignoreCase = 1
	// -1 表示以 NUL 终止的 UTF-16 字符串，避免把 UTF-8 字节数作为字符数。
	const nullTerminated = ^uintptr(0)
	result, _, _ := compareEnvironmentKeyOrdinal.Call(
		uintptr(unsafe.Pointer(left)), nullTerminated,
		uintptr(unsafe.Pointer(right)), nullTerminated, ignoreCase,
	)
	return result == ordinalEqual
}
