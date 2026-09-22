package context

import (
	stdcontext "context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
)

// Cleanup 幂等释放 multipart 临时文件和请求作用域，并等待全部资源实际关闭。
func (r *Request) Cleanup() error {
	return r.CleanupContext(stdcontext.Background())
}

// CleanupContext 启动一次完整清理，并允许调用方按上下文停止等待。
// 清理本身不会被丢弃：不合作的旧 Closer 会留在受监督的后台任务中；监督任务
// 等它真实返回后才按逆序关闭下一项，后续 Cleanup 会等待全部资源实际结束。
func (r *Request) CleanupContext(ctx stdcontext.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return ErrInvalidRequestCleanupContext
	}
	r.cleanupOnce.Do(func() {
		// 先原子封闭新解析入口，不能为了取得 formMu/serviceMu 而让请求 goroutine
		// 在监督器建立前无界等待正在执行的 multipart 或自定义解析器。
		r.cleanupStarted.Store(true)
		idleClosed, idleErr := r.cleanupIdleRequestScope()
		if idleClosed {
			r.formMu.Lock()
			r.cleanupErr = errors.Join(r.cleanupErr, idleErr)
			r.formMu.Unlock()
			return
		}
		r.cleanupDone = make(chan struct{})
		done := r.cleanupDone
		go r.cleanupResources(ctx, done, idleErr)
	})
	done := r.cleanupDone
	if done == nil {
		return r.requestCleanupError()
	}
	select {
	case <-done:
		return r.requestCleanupError()
	case <-ctx.Done():
		select {
		case <-done:
			return r.requestCleanupError()
		default:
			return ctx.Err()
		}
	}
}

// cleanupIdleRequestScope 为没有 multipart 临时文件且至多绑定一个空作用域的
// 普通请求提供同步零分配收尾；任何不确定情况都回退到完整有界清理。
func (r *Request) cleanupIdleRequestScope() (closed bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			closed = false
			err = fmt.Errorf("请求空闲作用域快速关闭 panic: %v", recovered)
		}
	}()
	if !r.formMu.TryLock() {
		return false, nil
	}
	defer r.formMu.Unlock()
	if r.raw != nil && r.raw.MultipartForm != nil {
		return false, nil
	}
	if !r.serviceMu.TryLock() {
		return false, nil
	}
	defer r.serviceMu.Unlock()
	if len(r.serviceClosers) > 1 {
		return false, nil
	}
	if len(r.serviceClosers) == 1 {
		idleCloser := r.serviceIdleCloser
		if isNilServiceScopeDependency(idleCloser) ||
			!sameServiceScopeDependency(r.serviceClosers[0], idleCloser) {
			return false, nil
		}
		idleClosed, idleErr := idleCloser.CloseIfIdle()
		if !idleClosed {
			return false, idleErr
		}
		err = idleErr
	}
	r.cleaned = true
	r.serviceResolver = nil
	r.serviceClosers = nil
	r.serviceIdleCloser = nil
	r.serviceClosed = true
	return true, err
}

func (r *Request) cleanupResources(
	ctx stdcontext.Context,
	done chan struct{},
	initialErr error,
) {
	cleanupErr := initialErr
	defer func() {
		if recovered := recover(); recovered != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("请求资源清理 panic: %v", recovered))
		}
		r.formMu.Lock()
		r.cleanupErr = cleanupErr
		close(done)
		r.formMu.Unlock()
	}()

	multipartForm := r.detachMultipartFormForCleanup()
	serviceClosers := r.detachServiceClosersForCleanup()

	if multipartForm != nil {
		cleanupErr = errors.Join(cleanupErr, runRequestCleanupTask(multipartForm.RemoveAll))
	}
	for index := len(serviceClosers) - 1; index >= 0; index-- {
		closer := serviceClosers[index]
		cleanupErr = errors.Join(cleanupErr, runRequestCleanupTask(func() error {
			return closeRequestService(ctx, closer)
		}))
	}
}

// detachMultipartFormForCleanup 在后台监督器内等待解析锁；defer 保证即使内部
// 状态访问 panic，顶层恢复前也不会遗留已持有的 formMu。
func (r *Request) detachMultipartFormForCleanup() *multipart.Form {
	r.formMu.Lock()
	defer r.formMu.Unlock()
	r.cleaned = true
	if r.raw == nil {
		return nil
	}
	multipartForm := r.raw.MultipartForm
	r.raw.MultipartForm = nil
	return multipartForm
}

// detachServiceClosersForCleanup 在后台监督器内等待在途 Make 退出，然后一次性
// 封闭服务状态；锁释放由 defer 保障，避免 panic 让 cleanupDone 永久悬挂。
func (r *Request) detachServiceClosersForCleanup() []io.Closer {
	r.serviceMu.Lock()
	defer r.serviceMu.Unlock()
	serviceClosers := append([]io.Closer(nil), r.serviceClosers...)
	r.serviceResolver = nil
	r.serviceClosers = nil
	r.serviceIdleCloser = nil
	r.serviceClosed = true
	return serviceClosers
}

func runRequestCleanupTask(task func() error) (taskErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			taskErr = fmt.Errorf("请求清理任务 panic: %v", recovered)
		}
	}()
	if task == nil {
		return nil
	}
	return task()
}

func closeRequestService(ctx stdcontext.Context, closer io.Closer) (err error) {
	if closer == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("关闭请求作用域 panic: %v", recovered)
		}
	}()
	if contextual, ok := closer.(interface {
		CloseContext(stdcontext.Context) error
	}); ok {
		contextErr := contextual.CloseContext(ctx)
		if ctx.Err() != nil && errors.Is(contextErr, ctx.Err()) {
			// CloseContext 只结束当前等待；继续调用幂等 Close，确保后台清理真实完成后
			// 请求的最终 Cleanup 才报告完成。
			return closer.Close()
		}
		return contextErr
	}
	return closer.Close()
}

func (r *Request) requestCleanupError() error {
	r.formMu.Lock()
	defer r.formMu.Unlock()
	return r.cleanupErr
}
