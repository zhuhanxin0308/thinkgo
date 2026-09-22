package driver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/zhuhanxin0308/thinkgo/framework/internal/winfile"
)

const (
	fileSessionLockWait   = 15 * time.Second
	fileSessionTempMaxAge = time.Hour
)

// File 采用受管哈希路径、原子替换和稳定的操作系统文件锁。
type File struct {
	path        string
	operationMu sync.RWMutex
	keyLocks    [fileSessionLockShardCount]sync.Mutex
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

// Read 与写入共享同一跨进程边界，避免身份复核观察到协作写者的中间替换。
func (f *File) Read(id string) (value string, found bool, result error) {
	if err := f.validate(id); err != nil {
		return "", false, err
	}
	f.operationMu.RLock()
	defer f.operationMu.RUnlock()
	lock := f.keyLock(id)
	lock.Lock()
	defer lock.Unlock()
	result = f.withCrossProcessLock(id, func() error {
		data, _, exists, err := f.readManagedFile(f.sessionFilePath(id), maxFileSessionEntryBytes)
		value, found = string(data), exists
		return err
	})
	return value, found, result
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
		_, err := f.removeManagedFile(f.sessionFilePath(id), time.Time{})
		return err
	})
}

// Clear 在全部分片锁内删除受管文件，保留稳定锁、无关文件与近期临时文件。
func (f *File) Clear() error {
	if f == nil || f.path == "" {
		return ErrInvalidSessionPath
	}
	f.operationMu.Lock()
	defer f.operationMu.Unlock()
	return f.withAllMutationGuards(func() error {
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
				info, err := entry.Info()
				if err != nil {
					result = errors.Join(result, ignoreSessionNotExist(err))
					continue
				}
				remove = now.Sub(info.ModTime()) >= fileSessionTempMaxAge
			}
			if remove {
				_, err := f.removeManagedFile(filepath.Join(f.path, name), time.Time{})
				result = errors.Join(result, err)
			}
		}
		return result
	})
}

// Update 的回调和提交持有同一内核锁，进程暂停不会因租约过期失去排他性。
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

// GC 在全部分片锁内重新读取修改时间，避免删除扫描后被另一进程刷新的 Session。
func (f *File) GC(maxLifetime time.Duration) (int, error) {
	if f == nil || f.path == "" {
		return 0, ErrInvalidSessionPath
	}
	if maxLifetime <= 0 {
		return 0, nil
	}
	f.operationMu.Lock()
	defer f.operationMu.Unlock()
	removed := 0
	err := f.withAllMutationGuards(func() error {
		entries, err := os.ReadDir(f.path)
		if err != nil {
			return err
		}
		deadline := time.Now().Add(-maxLifetime)
		var result error
		for _, entry := range entries {
			if !isManagedSessionFilename(entry.Name()) {
				continue
			}
			deleted, err := f.removeManagedFile(filepath.Join(f.path, entry.Name()), deadline)
			result = errors.Join(result, err)
			if deleted {
				removed++
			}
		}
		return result
	})
	return removed, err
}

func (f *File) removeManagedFile(path string, deadline time.Time) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, ignoreSessionNotExist(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("%w: %s", ErrUnsafeSessionFile, path)
	}
	if !deadline.IsZero() && !info.ModTime().Before(deadline) {
		return false, nil
	}
	return removeSessionFileIfSame(path, info)
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

func (f *File) mutationGuardPath(id string) string {
	digest := sha256.Sum256([]byte(id))
	return f.shardGuardPath(int(digest[0]) % fileSessionLockShardCount)
}

func (f *File) shardGuardPath(shard int) string {
	return filepath.Join(f.path, fmt.Sprintf(".session-mutation-%02x.guard", shard))
}

func (f *File) keyLock(id string) *sync.Mutex {
	digest := sha256.Sum256([]byte(id))
	return &f.keyLocks[int(digest[0])%len(f.keyLocks)]
}

func (f *File) withCrossProcessLock(id string, operation func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), fileSessionLockWait)
	defer cancel()
	return sessionGuardError(winfile.WithGuard(ctx, f.mutationGuardPath(id), operation))
}

// 全量操作固定按分片升序获取锁，所有等待共享一个截止时间，避免死锁和超时倍增。
func (f *File) withAllMutationGuards(operation func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), fileSessionLockWait)
	defer cancel()
	var acquire func(int) error
	acquire = func(shard int) error {
		if shard == fileSessionLockShardCount {
			return operation()
		}
		return winfile.WithGuard(ctx, f.shardGuardPath(shard), func() error { return acquire(shard + 1) })
	}
	return sessionGuardError(acquire(0))
}

func sessionGuardError(err error) error {
	if errors.Is(err, winfile.ErrUnsafeGuard) {
		return errors.Join(ErrUnsafeSessionFile, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(ErrSessionLockTimeout, err)
	}
	return err
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
	return readManagedFileWithOpen(path, limit, openSessionFile)
}

func readManagedFileWithOpen(path string, limit int64, open func(string) (*os.File, error)) ([]byte, os.FileInfo, bool, error) {
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
	handle, err := open(path)
	if err != nil {
		return nil, linkInfo, false, err
	}
	openedInfo, statErr := handle.Stat()
	currentInfo, verifyErr := os.Lstat(path)
	if verifyErr == nil && currentInfo != nil &&
		(currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.Mode().IsRegular()) {
		_ = handle.Close()
		return nil, currentInfo, false, ErrUnsafeSessionFile
	}
	if statErr != nil || verifyErr != nil || openedInfo == nil || currentInfo == nil ||
		!openedInfo.Mode().IsRegular() {
		_ = handle.Close()
		return nil, openedInfo, false, errors.Join(ErrUnsafeSessionFile, statErr, verifyErr)
	}
	if !os.SameFile(openedInfo, currentInfo) {
		_ = handle.Close()
		return nil, currentInfo, false, ErrUnsafeSessionFile
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
	// 同目录原子替换保留临时文件已经设置的 0600/DACL 权限。
	return nil
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
