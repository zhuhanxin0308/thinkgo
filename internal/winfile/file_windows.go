//go:build windows

// Package winfile 提供稳定文件锁、文件身份核验和原子替换。
package winfile

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const shareMode = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE

var ErrUnsafeFile = errors.New("文件身份不是安全的普通文件")

// Open 统一受管文件的共享模式和原生错误路径。
func Open(path string, access, disposition uint32) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(name, access, shareMode, nil, disposition, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// Matches 确认句柄、期望身份和当前路径指向同一普通文件；调用方继续使用该句柄执行操作。
func Matches(path string, handle *os.File, expected os.FileInfo) (os.FileInfo, bool, error) {
	opened, err := handle.Stat()
	if err != nil {
		return nil, false, err
	}
	if !opened.Mode().IsRegular() {
		return nil, false, ErrUnsafeFile
	}
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return opened, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() {
		return nil, false, ErrUnsafeFile
	}
	return opened, expected != nil && os.SameFile(expected, opened) && os.SameFile(opened, current), nil
}

// Delete 在核验过的句柄上设置删除标记，不重新按路径定位目标。
func Delete(handle *os.File) error {
	deleteEnabled := byte(1)
	err := windows.SetFileInformationByHandle(windows.Handle(handle.Fd()), windows.FileDispositionInfo, &deleteEnabled, uint32(unsafe.Sizeof(deleteEnabled)))
	runtime.KeepAlive(handle)
	return err
}

// RemoveIfSame 原子地绑定身份核验与删除对象，不删除已被替换的新文件。
func RemoveIfSame(path string, expected os.FileInfo) (removed bool, returnErr error) {
	handle, err := Open(path, windows.GENERIC_READ|windows.DELETE, windows.OPEN_EXISTING)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, handle.Close()) }()
	_, matches, err := Matches(path, handle, expected)
	if err != nil || !matches {
		return false, err
	}
	if err := Delete(handle); err != nil {
		return false, &os.PathError{Op: "remove", Path: path, Err: err}
	}
	return true, nil
}

// Replace 使用 POSIX 替换语义，允许共享删除读者继续读取旧文件。
// MoveFileEx 即使面对 FILE_SHARE_DELETE 句柄仍可能返回拒绝访问，不能靠重试保证正确性。
func Replace(source, target string) error {
	if err := replaceWithOpenReaders(source, target); err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: target, Err: err}
	}
	return nil
}

func replaceWithOpenReaders(source, target string) (result error) {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	name, err := windows.UTF16FromString(absolute)
	if err != nil {
		return err
	}
	nameUnits := len(name)
	if nameUnits < 1 || nameUnits > windows.MAX_LONG_PATH {
		return windows.ERROR_FILENAME_EXCED_RANGE
	}
	handle, err := Open(source, windows.DELETE, windows.OPEN_EXISTING)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, handle.Close()) }()
	type renameInformation struct {
		flags      uint32
		_          windows.Handle
		nameLength uint32
		name       [1]uint16
	}
	var layout renameInformation
	nameBytes := (uint64(nameUnits) - 1) * 2
	// Win32 路径转换仍需要终止符；FileNameLength 不包含它，但缓冲区必须保留它。
	nameOffset := int(unsafe.Offsetof(layout.name))
	buffer := make([]byte, nameOffset+len(name)*2)
	bufferSize := uint64(len(buffer))
	if nameBytes > math.MaxUint32 || bufferSize > math.MaxUint32 {
		return windows.ERROR_FILENAME_EXCED_RANGE
	}
	// 使用 ABI 字段偏移编码字节，避免把变长缓冲区强转为越界结构体或切片。
	binary.LittleEndian.PutUint32(buffer[int(unsafe.Offsetof(layout.flags)):], windows.FILE_RENAME_REPLACE_IF_EXISTS|windows.FILE_RENAME_POSIX_SEMANTICS)
	binary.LittleEndian.PutUint32(buffer[int(unsafe.Offsetof(layout.nameLength)):], uint32(nameBytes))
	for index, unit := range name {
		binary.LittleEndian.PutUint16(buffer[nameOffset+index*2:], unit)
	}
	err = windows.SetFileInformationByHandle(windows.Handle(handle.Fd()), windows.FileRenameInfoEx, &buffer[0], uint32(bufferSize))
	runtime.KeepAlive(handle)
	// 仅不支持此信息类或语义的旧文件系统回退；权限和共享错误保留原始原因。
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) || errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
		return windows.Rename(source, target)
	}
	return err
}
