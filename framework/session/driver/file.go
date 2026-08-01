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
	// 锁等待上限覆盖 Windows 高竞争下的文件删除/重建窗口，避免正常排队被误判为失效。
	fileSessionLockWait        = 15 * time.Second
	fileSessionLockLifetime    = 30 * time.Second
	fileSessionLockRetry       = 10 * time.Millisecond
	fileSessionLockRetrySlots  = 32
	fileSessionLockRetryMix    = uint32(0x9e3779b9)
	fileSessionLockRetryMixA   = uint32(0x21f0aaad)
	fileSessionLockRetryMixB   = uint32(0x735a2d97)
	fileSessionLockRetryShiftA = 16
	fileSessionLockRetryShiftB = 15
	fileSessionLockRetryShiftC = 15
	fileSessionLockFNVOffset   = uint32(2166136261)
	fileSessionLockFNVPrime    = uint32(16777619)
	fileSessionCorruptLockAge  = time.Minute
	fileSessionTempMaxAge      = time.Hour
	maxFileSessionLockBytes    = 1024
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
	return readManagedFileWithOpen(path, limit, os.Open)
}

func readManagedLockFile(path string, limit int64) ([]byte, os.FileInfo, bool, error) {
	return readManagedFileWithOpen(path, limit, openLockFile)
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
	retrySeed := fileSessionLockRetrySeed(owner)
	retryAttempt := uint32(0)
	deadlineGraceUsed := false
	var accessDeniedErr error
	for {
		now := time.Now()
		handle, err := createLockFile(path)
		if err == nil {
			return initializeCreatedLockFile(path, owner, handle, now, writeSessionData)
		}
		if !errors.Is(err, os.ErrExist) {
			if isLockFileAccessDenied(err) {
				accessDeniedErr = err
				observed, observeErr := os.Lstat(path)
				if observeErr == nil {
					if observed.Mode()&os.ModeSymlink != 0 || !observed.Mode().IsRegular() {
						return nil, ErrUnsafeSessionFile
					}
					err = os.ErrExist
				} else if errors.Is(observeErr, os.ErrNotExist) || isLockFileAccessDenied(observeErr) {
					if deadlineErr := fileSessionLockDeadlineError(now, deadline, accessDeniedErr); deadlineErr != nil {
						if fileSessionLockCanRetryMissingAtDeadline(now, deadline, errors.Is(observeErr, os.ErrNotExist), &deadlineGraceUsed) {
							continue
						}
						return nil, deadlineErr
					}
					time.Sleep(fileSessionLockRetryDelay(retrySeed, retryAttempt))
					retryAttempt++
					continue
				} else {
					return nil, err
				}
			} else if isLockFileCreateTransient(err) {
				if deadlineErr := fileSessionLockDeadlineError(now, deadline, accessDeniedErr); deadlineErr != nil {
					return nil, deadlineErr
				}
				time.Sleep(fileSessionLockRetryDelay(retrySeed, retryAttempt))
				retryAttempt++
				continue
			}
			if !errors.Is(err, os.ErrExist) {
				return nil, err
			}
		}

		data, info, found, readErr := readManagedLockFile(path, maxFileSessionLockBytes)
		accessDeniedErr = fileSessionLockAccessDeniedAfterRead(accessDeniedErr, found, readErr)
		if readErr != nil || !found {
			if isLockFileAccessDenied(readErr) {
				if info != nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
					return nil, errors.Join(ErrUnsafeSessionFile, readErr)
				}
				if deadlineErr := fileSessionLockDeadlineError(now, deadline, accessDeniedErr); deadlineErr != nil {
					return nil, deadlineErr
				}
				time.Sleep(fileSessionLockRetryDelay(retrySeed, retryAttempt))
				retryAttempt++
				continue
			}
			if info != nil && now.Sub(info.ModTime()) >= fileSessionCorruptLockAge {
				removed, removeErr := removeOwnedLockFile(path, "", info)
				if removeErr != nil {
					return nil, removeErr
				}
				if removed {
					continue
				}
			}
			if lockObservationMayBeTransient(info, found, readErr) {
				if deadlineErr := fileSessionLockDeadlineError(now, deadline, accessDeniedErr); deadlineErr != nil {
					if fileSessionLockCanRetryMissingAtDeadline(now, deadline, !found && (readErr == nil || errors.Is(readErr, os.ErrNotExist)), &deadlineGraceUsed) {
						continue
					}
					return nil, deadlineErr
				}
				time.Sleep(fileSessionLockRetryDelay(retrySeed, retryAttempt))
				retryAttempt++
				continue
			}
			return nil, errors.Join(ErrUnsafeSessionFile, readErr)
		}
		var payload fileSessionLock
		if unmarshalErr := json.Unmarshal(data, &payload); unmarshalErr != nil || payload.Owner == "" || payload.ExpireAt <= 0 {
			if now.Sub(info.ModTime()) >= fileSessionCorruptLockAge {
				removed, removeErr := removeOwnedLockFile(path, "", info)
				if removeErr != nil {
					return nil, removeErr
				}
				if removed {
					continue
				}
			}
			if lockPayloadMayBeIncomplete(data, unmarshalErr) {
				if deadlineErr := fileSessionLockDeadlineError(now, deadline, accessDeniedErr); deadlineErr != nil {
					return nil, deadlineErr
				}
				time.Sleep(fileSessionLockRetryDelay(retrySeed, retryAttempt))
				retryAttempt++
				continue
			}
			return nil, fmt.Errorf("%w: 锁载荷损坏", ErrUnsafeSessionFile)
		}
		if payload.ExpireAt <= now.UnixNano() {
			removed, removeErr := removeOwnedLockFile(path, payload.Owner, info)
			if removeErr != nil {
				return nil, removeErr
			}
			if removed {
				continue
			}
		}
		if deadlineErr := fileSessionLockDeadlineError(now, deadline, accessDeniedErr); deadlineErr != nil {
			return nil, deadlineErr
		}
		time.Sleep(fileSessionLockRetryDelay(retrySeed, retryAttempt))
		retryAttempt++
	}
}

// fileSessionLockCanRetryMissingAtDeadline 仅为已确认锁文件缺失的瞬态提供一次截止宽限获取机会。
// 这不会延长有效锁、权限错误或损坏载荷的等待时间，也不会形成无界重试。
func fileSessionLockCanRetryMissingAtDeadline(now, deadline time.Time, missing bool, used *bool) bool {
	if !missing || now.Before(deadline) || used == nil || *used {
		return false
	}
	*used = true
	return true
}

// initializeCreatedLockFile 立即绑定创建句柄身份，并仅在身份与 owner 复核成功后返回锁。
func initializeCreatedLockFile(
	path, owner string,
	handle *os.File,
	now time.Time,
	write func(io.Writer, []byte) error,
) (os.FileInfo, error) {
	createdInfo, statErr := handle.Stat()
	if statErr != nil {
		return nil, errors.Join(statErr, discardUnidentifiedCreatedLockFile(path, handle))
	}
	payload, marshalErr := json.Marshal(fileSessionLock{
		Owner: owner, ExpireAt: now.Add(fileSessionLockLifetime).UnixNano(),
	})
	writeErr := restrictSessionPath(path, false)
	if marshalErr == nil && writeErr == nil {
		writeErr = write(handle, payload)
	}
	if writeErr == nil {
		writeErr = handle.Sync()
	}
	closeErr := handle.Close()
	if combined := errors.Join(marshalErr, writeErr, closeErr); combined != nil {
		_, cleanupErr := removeOwnedLockFile(path, "", createdInfo)
		return nil, errors.Join(combined, cleanupErr)
	}
	owned, verifyErr := lockFileMatchesOwner(path, owner, createdInfo)
	if verifyErr != nil {
		_, cleanupErr := removeOwnedLockFile(path, "", createdInfo)
		return nil, errors.Join(verifyErr, cleanupErr)
	}
	if !owned {
		_, cleanupErr := removeOwnedLockFile(path, "", createdInfo)
		return nil, errors.Join(ErrUnsafeSessionFile, cleanupErr)
	}
	return createdInfo, nil
}

// fileSessionLockRetrySeed 为单次获取预计算 owner 的 FNV-1a 种子。
func fileSessionLockRetrySeed(owner string) uint32 {
	seed := fileSessionLockFNVOffset
	for index := 0; index < len(owner); index++ {
		seed ^= uint32(owner[index])
		seed *= fileSessionLockFNVPrime
	}
	return seed
}

// fileSessionLockRetryDelay 使用 SplitMix 风格的 avalanche 混合生成有界偏移，
// 让初始槽相同的等待者也能在后续重试中快速分离，避免高竞争下饥饿。
func fileSessionLockRetryDelay(seed, attempt uint32) time.Duration {
	mixed := seed + attempt*fileSessionLockRetryMix
	mixed = (mixed ^ (mixed >> fileSessionLockRetryShiftA)) * fileSessionLockRetryMixA
	mixed = (mixed ^ (mixed >> fileSessionLockRetryShiftB)) * fileSessionLockRetryMixB
	mixed ^= mixed >> fileSessionLockRetryShiftC
	slot := mixed % fileSessionLockRetrySlots
	return fileSessionLockRetry + time.Duration(slot)*time.Millisecond
}

// fileSessionLockAccessDeniedAfterRead 在稳定读取到锁后清除已过期的拒绝访问错误。
func fileSessionLockAccessDeniedAfterRead(previous error, found bool, readErr error) error {
	if found && readErr == nil {
		return nil
	}
	if isLockFileAccessDenied(readErr) {
		return readErr
	}
	return previous
}

// fileSessionLockDeadlineError 在获取截止后保留最近的拒绝访问路径错误，其他情况返回锁超时。
func fileSessionLockDeadlineError(now, deadline time.Time, accessDeniedErr error) error {
	if now.Before(deadline) {
		return nil
	}
	if accessDeniedErr != nil {
		return accessDeniedErr
	}
	return ErrSessionLockTimeout
}

func lockObservationMayBeTransient(info os.FileInfo, found bool, err error) bool {
	if !found && err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if isLockFileReadTransient(err) {
		return true
	}
	return info != nil && info.Mode().IsRegular() && errors.Is(err, ErrUnsafeSessionFile)
}

func lockPayloadMayBeIncomplete(data []byte, err error) bool {
	var syntaxErr *json.SyntaxError
	return errors.As(err, &syntaxErr) && syntaxErr.Offset >= int64(len(data))
}

func (f *File) releaseCrossProcessLock(id, owner string, expected os.FileInfo) error {
	path := f.lockFilePath(id)
	_, err := removeOwnedLockFile(path, owner, expected)
	return err
}

func lockFileMatchesOwner(path, owner string, expected os.FileInfo) (bool, error) {
	data, current, found, err := readManagedLockFile(path, maxFileSessionLockBytes)
	if expected != nil && current != nil && !os.SameFile(expected, current) {
		return false, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !found {
		return false, err
	}
	if expected == nil {
		return false, nil
	}
	if owner == "" {
		return true, nil
	}
	var payload fileSessionLock
	if err = json.Unmarshal(data, &payload); err != nil {
		return false, err
	}
	return payload.Owner == owner, nil
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
