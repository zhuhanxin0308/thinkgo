package driver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	fileSessionLockWait       = 5 * time.Second
	fileSessionLockLifetime   = 30 * time.Second
	fileSessionLockRetry      = 10 * time.Millisecond
	fileSessionCorruptLockAge = time.Minute
	fileSessionTempMaxAge     = time.Hour
	maxFileSessionLockBytes   = 1024
)

// File 是采用哈希受管路径、原子替换和跨进程锁的文件 Session 驱动。
type File struct {
	path        string
	operationMu sync.RWMutex
	keyLocks    [fileSessionLockShardCount]sync.Mutex
}

type fileSessionLock struct {
	Owner    string `json:"owner"`
	ExpireAt int64  `json:"expire_at"`
}

// NewFile 创建并验证文件 Session 根目录。
func NewFile(path string) (*File, error) {
	path = strings.TrimSpace(path)
	if path == "" || containsControlCharacter(path) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidSessionPath, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSessionPath, err)
	}
	if err = os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSessionPath, err)
	}
	realPath, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSessionPath, err)
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: %q: %v", ErrInvalidSessionPath, realPath, err)
	}
	if err = restrictSessionPath(realPath, true); err != nil {
		return nil, fmt.Errorf("%w: 限制目录权限失败: %v", ErrInvalidSessionPath, err)
	}
	return &File{path: filepath.Clean(realPath)}, nil
}

// Read 安全读取单个受管文件，并区分缺失与空内容。
func (f *File) Read(id string) (string, bool, error) {
	if err := f.validate(id); err != nil {
		return "", false, err
	}
	f.operationMu.RLock()
	defer f.operationMu.RUnlock()
	lock := f.keyLock(id)
	lock.Lock()
	defer lock.Unlock()
	data, _, found, err := f.readManagedFile(f.sessionFilePath(id), maxFileSessionEntryBytes)
	if err != nil || !found {
		return "", found, err
	}
	return string(data), true, nil
}

// Write 在跨进程排他区间内原子替换 Session 文件。
func (f *File) Write(id string, data string) error {
	if err := f.validate(id); err != nil {
		return err
	}
	if len(data) > maxFileSessionEntryBytes {
		return fmt.Errorf("%w: %d", ErrSessionEntryTooLarge, len(data))
	}
	f.operationMu.RLock()
	defer f.operationMu.RUnlock()
	lock := f.keyLock(id)
	lock.Lock()
	defer lock.Unlock()
	return f.withCrossProcessLock(id, func() error {
		if err := f.validateExistingTarget(f.sessionFilePath(id)); err != nil {
			return err
		}
		return f.writeManagedFile(f.sessionFilePath(id), []byte(data))
	})
}

// Delete 幂等删除受管 Session，且拒绝符号链接或身份异常文件。
func (f *File) Delete(id string) error {
	if err := f.validate(id); err != nil {
		return err
	}
	f.operationMu.RLock()
	defer f.operationMu.RUnlock()
	lock := f.keyLock(id)
	lock.Lock()
	defer lock.Unlock()
	return f.withCrossProcessLock(id, func() error {
		path := f.sessionFilePath(id)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return ErrUnsafeSessionFile
		}
		_, err = removeSessionFileIfSame(path, info)
		return err
	})
}

// Clear 删除全部受管 .session 文件和陈旧临时文件，保留其它目录内容及锁文件。
func (f *File) Clear() error {
	if f == nil || f.path == "" {
		return ErrInvalidSessionPath
	}
	f.operationMu.Lock()
	defer f.operationMu.Unlock()
	entries, err := os.ReadDir(f.path)
	if err != nil {
		return err
	}
	now := time.Now()
	var result error
	for _, entry := range entries {
		name := entry.Name()
		remove := isManagedSessionFilename(name)
		if strings.HasPrefix(name, ".tmp-session-") {
			if info, infoErr := entry.Info(); infoErr != nil {
				result = errors.Join(result, infoErr)
				continue
			} else if now.Sub(info.ModTime()) >= fileSessionTempMaxAge {
				remove = true
			}
		}
		if !remove {
			continue
		}
		path := filepath.Join(f.path, name)
		info, lstatErr := os.Lstat(path)
		if lstatErr != nil {
			result = errors.Join(result, ignoreSessionNotExist(lstatErr))
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			result = errors.Join(result, fmt.Errorf("%w: %s", ErrUnsafeSessionFile, name))
			continue
		}
		_, removeErr := removeSessionFileIfSame(path, info)
		result = errors.Join(result, removeErr)
	}
	return result
}

// Update 在同一进程分片锁与跨进程锁内完成原子读改写。
func (f *File) Update(id string, update func(string, bool) (string, bool, error)) error {
	if err := f.validate(id); err != nil {
		return err
	}
	if update == nil {
		return ErrInvalidSessionUpdate
	}
	f.operationMu.RLock()
	defer f.operationMu.RUnlock()
	lock := f.keyLock(id)
	lock.Lock()
	defer lock.Unlock()
	return f.withCrossProcessLock(id, func() error {
		path := f.sessionFilePath(id)
		data, info, found, err := f.readManagedFile(path, maxFileSessionEntryBytes)
		if err != nil {
			return err
		}
		next, remove, err := update(string(data), found)
		if err != nil {
			return err
		}
		if remove {
			if !found {
				return nil
			}
			_, err = removeSessionFileIfSame(path, info)
			return err
		}
		if len(next) > maxFileSessionEntryBytes {
			return fmt.Errorf("%w: %d", ErrSessionEntryTooLarge, len(next))
		}
		return f.writeManagedFile(path, []byte(next))
	})
}

// GC 回收超过 maxLifetime 未修改的受管 Session 文件。
func (f *File) GC(maxLifetime time.Duration) (int, error) {
	if f == nil || f.path == "" {
		return 0, ErrInvalidSessionPath
	}
	if maxLifetime <= 0 {
		return 0, nil
	}
	f.operationMu.Lock()
	defer f.operationMu.Unlock()
	entries, err := os.ReadDir(f.path)
	if err != nil {
		return 0, err
	}
	deadline := time.Now().Add(-maxLifetime)
	removed := 0
	var result error
	for _, entry := range entries {
		if !isManagedSessionFilename(entry.Name()) {
			continue
		}
		path := filepath.Join(f.path, entry.Name())
		info, infoErr := os.Lstat(path)
		if infoErr != nil {
			result = errors.Join(result, ignoreSessionNotExist(infoErr))
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			result = errors.Join(result, fmt.Errorf("%w: %s", ErrUnsafeSessionFile, entry.Name()))
			continue
		}
		if !info.ModTime().Before(deadline) {
			continue
		}
		deleted, removeErr := removeSessionFileIfSame(path, info)
		result = errors.Join(result, removeErr)
		if deleted {
			removed++
		}
	}
	return removed, result
}

func (f *File) validate(id string) error {
	if f == nil || f.path == "" {
		return ErrInvalidSessionPath
	}
	return validateSessionID(id)
}

func (f *File) sessionFilePath(id string) string {
	return filepath.Join(f.path, hashedSessionFilename(id, ".session"))
}

func (f *File) lockFilePath(id string) string {
	return filepath.Join(f.path, hashedSessionFilename(id, ".lock"))
}

func (f *File) keyLock(id string) *sync.Mutex {
	digest := sha256.Sum256([]byte(id))
	return &f.keyLocks[int(digest[0])%len(f.keyLocks)]
}

func (f *File) validateExistingTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrUnsafeSessionFile
	}
	return nil
}

func (f *File) readManagedFile(path string, limit int64) ([]byte, os.FileInfo, bool, error) {
	linkInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, linkInfo, false, err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.Mode().IsRegular() {
		return nil, linkInfo, false, ErrUnsafeSessionFile
	}
	handle, err := os.Open(path)
	if err != nil {
		return nil, linkInfo, false, err
	}
	openedInfo, statErr := handle.Stat()
	currentInfo, verifyErr := os.Lstat(path)
	if statErr != nil || verifyErr != nil || !openedInfo.Mode().IsRegular() ||
		currentInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, currentInfo) {
		_ = handle.Close()
		return nil, openedInfo, false, errors.Join(ErrUnsafeSessionFile, statErr, verifyErr)
	}
	if openedInfo.Size() < 0 || openedInfo.Size() > limit {
		_ = handle.Close()
		return nil, openedInfo, false, fmt.Errorf("%w: %d", ErrSessionEntryTooLarge, openedInfo.Size())
	}
	data, readErr := io.ReadAll(io.LimitReader(handle, limit+1))
	closeErr := handle.Close()
	if int64(len(data)) > limit {
		return nil, openedInfo, false, ErrSessionEntryTooLarge
	}
	if combined := errors.Join(readErr, closeErr); combined != nil {
		return nil, openedInfo, false, combined
	}
	return data, openedInfo, true, nil
}

func (f *File) writeManagedFile(target string, data []byte) error {
	temporary, err := os.CreateTemp(f.path, ".tmp-session-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
	}
	if err = restrictSessionPath(temporaryName, false); err != nil {
		cleanup()
		return err
	}
	if err = writeSessionData(temporary, data); err != nil {
		cleanup()
		return err
	}
	if err = temporary.Sync(); err != nil {
		cleanup()
		return err
	}
	if err = temporary.Close(); err != nil {
		_ = os.Remove(temporaryName)
		return err
	}
	if err = replaceSessionFile(temporaryName, target); err != nil {
		_ = os.Remove(temporaryName)
		return err
	}
	// 同目录原子替换会保留临时文件已经设置的 0600/DACL 权限。
	return nil
}

func (f *File) withCrossProcessLock(id string, operation func() error) (result error) {
	owner, err := newSessionLockOwner()
	if err != nil {
		return err
	}
	lockInfo, err := f.acquireCrossProcessLock(id, owner)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, f.releaseCrossProcessLock(id, owner, lockInfo))
	}()
	return operation()
}

func (f *File) acquireCrossProcessLock(id, owner string) (os.FileInfo, error) {
	path := f.lockFilePath(id)
	deadline := time.Now().Add(fileSessionLockWait)
	for {
		now := time.Now()
		handle, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			payload, marshalErr := json.Marshal(fileSessionLock{
				Owner: owner, ExpireAt: now.Add(fileSessionLockLifetime).UnixNano(),
			})
			writeErr := restrictSessionPath(path, false)
			if marshalErr == nil && writeErr == nil {
				writeErr = writeSessionData(handle, payload)
			}
			if writeErr == nil {
				writeErr = handle.Sync()
			}
			closeErr := handle.Close()
			if combined := errors.Join(marshalErr, writeErr, closeErr); combined != nil {
				_ = os.Remove(path)
				return nil, combined
			}
			info, statErr := os.Lstat(path)
			return info, statErr
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}

		data, info, found, readErr := f.readManagedFile(path, maxFileSessionLockBytes)
		if readErr != nil || !found {
			if info != nil && now.Sub(info.ModTime()) >= fileSessionCorruptLockAge {
				removed, removeErr := removeSessionFileIfSame(path, info)
				if removeErr != nil {
					return nil, removeErr
				}
				if removed {
					continue
				}
			}
			return nil, errors.Join(ErrUnsafeSessionFile, readErr)
		}
		var payload fileSessionLock
		if unmarshalErr := json.Unmarshal(data, &payload); unmarshalErr != nil || payload.Owner == "" || payload.ExpireAt <= 0 {
			if now.Sub(info.ModTime()) >= fileSessionCorruptLockAge {
				removed, removeErr := removeSessionFileIfSame(path, info)
				if removeErr != nil {
					return nil, removeErr
				}
				if removed {
					continue
				}
			}
			return nil, fmt.Errorf("%w: 锁载荷损坏", ErrUnsafeSessionFile)
		}
		if payload.ExpireAt <= now.UnixNano() {
			removed, removeErr := removeSessionFileIfSame(path, info)
			if removeErr != nil {
				return nil, removeErr
			}
			if removed {
				continue
			}
		}
		if !now.Before(deadline) {
			return nil, ErrSessionLockTimeout
		}
		time.Sleep(fileSessionLockRetry)
	}
}

func (f *File) releaseCrossProcessLock(id, owner string, expected os.FileInfo) error {
	path := f.lockFilePath(id)
	data, current, found, err := f.readManagedFile(path, maxFileSessionLockBytes)
	if err != nil || !found {
		return err
	}
	if expected == nil || !os.SameFile(expected, current) {
		return nil
	}
	var payload fileSessionLock
	if err = json.Unmarshal(data, &payload); err != nil {
		return err
	}
	if payload.Owner != owner {
		return nil
	}
	_, err = removeSessionFileIfSame(path, current)
	return err
}

func newSessionLockOwner() (string, error) {
	data := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func hashedSessionFilename(id, suffix string) string {
	digest := sha256.Sum256([]byte(id))
	return "sess_" + hex.EncodeToString(digest[:]) + suffix
}

func isManagedSessionFilename(name string) bool {
	if !strings.HasPrefix(name, "sess_") || !strings.HasSuffix(name, ".session") {
		return false
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(name, "sess_"), ".session")
	if len(digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func writeSessionData(writer io.Writer, data []byte) error {
	written, err := writer.Write(data)
	if err == nil && written != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func removeSessionFileIfSame(path string, expected os.FileInfo) (bool, error) {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if expected == nil || !os.SameFile(expected, current) {
		return false, nil
	}
	if err = os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

func ignoreSessionNotExist(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func containsControlCharacter(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
