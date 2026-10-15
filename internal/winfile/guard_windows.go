//go:build windows

package winfile

import (
	"context"
	"errors"
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

func openGuard(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// 稳定锁文件不参与删除共享，持有或等待锁期间禁止替换锁的身份。
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// guardRequest owns the native completion state until its I/O has completed.
// The event's low bit suppresses IOCP notifications, including inline success,
// so os.NewFile's runtime registration never receives our OVERLAPPED address.
type guardRequest struct {
	overlapped *windows.Overlapped
	event      windows.Handle
	pinner     runtime.Pinner
}

func newGuardRequest() (*guardRequest, error) {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, os.NewSyscallError("CreateEvent", err)
	}
	request := &guardRequest{
		overlapped: &windows.Overlapped{HEvent: event | 1},
		event:      event,
	}
	request.pinner.Pin(request.overlapped)
	return request, nil
}

func (request *guardRequest) complete(native windows.Handle, err error) error {
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		return err
	}
	var transferred uint32
	return windows.GetOverlappedResult(native, request.overlapped, &transferred, true)
}

// close is safe only after completion and after any cancellation callback ends.
func (request *guardRequest) close() error {
	request.pinner.Unpin()
	return windows.CloseHandle(request.event)
}

// lockGuard submits one native waiting request instead of repeatedly racing for
// a newly unlocked range. Context cancellation requests cancellation, then joins
// completion; the kernel may still grant the lock while cancellation is racing.
func lockGuard(ctx context.Context, handle *os.File) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	request, err := newGuardRequest()
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(handle)
	acquired := false
	defer func() {
		result = errors.Join(result, request.close())
		if acquired && result != nil {
			result = errors.Join(result, unlockGuard(handle))
		}
	}()
	// Fd may disassociate the runtime IOCP; fetch it before submitting I/O and
	// reuse this handle throughout cancellation and completion.
	native := windows.Handle(handle.Fd())
	err = windows.LockFileEx(native, windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, 1, 0, request.overlapped)
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		acquired = err == nil
		return err
	}

	// Register only after submission. An already canceled context then cancels
	// the actual pending request, rather than missing an operation issued later.
	cancelDone := make(chan struct{})
	var cancelErr error
	stopCancel := context.AfterFunc(ctx, func() {
		cancelErr = windows.CancelIoEx(native, request.overlapped)
		close(cancelDone)
	})
	completionErr := request.complete(native, err)
	if !stopCancel() {
		<-cancelDone
	}
	if errors.Is(cancelErr, windows.ERROR_NOT_FOUND) {
		// Completion can win the race before CancelIoEx locates the request.
		cancelErr = nil
	}
	acquired = completionErr == nil
	return errors.Join(ctx.Err(), completionErr, cancelErr)
}

func tryGuardLock(handle *os.File) (locked bool, result error) {
	request, err := newGuardRequest()
	if err != nil {
		return false, err
	}
	defer runtime.KeepAlive(handle)
	defer func() { result = errors.Join(result, request.close()) }()
	native := windows.Handle(handle.Fd())
	err = windows.LockFileEx(native,
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, request.overlapped)
	// FAIL_IMMEDIATELY does not queue for an unavailable range. If a driver
	// nevertheless reports pending I/O, cancel it before joining completion;
	// a nonblocking probe must not turn into an uncanceled lock waiter.
	pending := errors.Is(err, windows.ERROR_IO_PENDING)
	var cancelErr error
	if pending {
		cancelErr = windows.CancelIoEx(native, request.overlapped)
		err = request.complete(native, err)
		if errors.Is(cancelErr, windows.ERROR_NOT_FOUND) {
			cancelErr = nil
		}
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		(pending && errors.Is(err, windows.ERROR_OPERATION_ABORTED)) {
		return false, cancelErr
	}
	// Cancellation can lose to a successful grant. Report ownership so the
	// caller releases that lock rather than treating it as an unsuccessful try.
	return err == nil, errors.Join(err, cancelErr)
}

func unlockGuard(handle *os.File) (result error) {
	request, err := newGuardRequest()
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(handle)
	defer func() { result = errors.Join(result, request.close()) }()
	native := windows.Handle(handle.Fd())
	err = windows.UnlockFileEx(native, 0, 1, 0, request.overlapped)
	// UnlockFileEx also supports asynchronous handles. Never cancel release
	// because the acquiring context expired; wait until release has completed.
	return request.complete(native, err)
}
