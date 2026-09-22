package http

import (
	stdcontext "context"
	"errors"
	"net/http"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

// acquireRequestTask 在任何业务执行和请求资源构造之前预约后台收尾容量。
func (h *Http) acquireRequestTask(raw *http.Request) (*framework.RequestTask, error) {
	capacity := h.srvConf.MaxOutstandingRequests
	if capacity <= 0 {
		capacity = defaultMaxOutstandingRequests
	}
	timeout := h.srvConf.ShutdownTimeout
	if timeout <= 0 {
		timeout = defaultRequestEndTimeout
	}
	if raw != nil && raw.URL != nil {
		path := raw.URL.Path
		if application, exists := resolvedApplication(raw); exists {
			path = application.RewrittenPath()
		}
		if h.app.IsOperationalPath(path) {
			return h.app.AcquireOperationalRequestTask(timeout)
		}
	}
	return h.app.AcquireRequestTask(capacity, timeout)
}

type requestTaskEntry struct {
	task        *framework.RequestTask
	hostManaged bool
}

func (h *Http) rememberRequestTask(request *fwcontext.Request, task *framework.RequestTask, hostManaged bool) {
	h.endStateMu.Lock()
	if h.requestTasks == nil {
		h.requestTasks = make(map[*fwcontext.Request]requestTaskEntry)
	}
	h.requestTasks[request] = requestTaskEntry{task: task, hostManaged: hostManaged}
	h.endStateMu.Unlock()
}

// requestTaskHostManaged 区分外层 ServeHTTP 的清理责任和直接 Run 的异常责任。
func (h *Http) requestTaskHostManaged(request *fwcontext.Request) bool {
	h.endStateMu.Lock()
	defer h.endStateMu.Unlock()
	return h.requestTasks[request].hostManaged
}

// ensureRequestTask 补齐直接 Run 的租约；ServeHTTP 已经登记的请求不重复占用容量。
func (h *Http) ensureRequestTask(request *fwcontext.Request) error {
	h.endStateMu.Lock()
	_, exists := h.requestTasks[request]
	h.endStateMu.Unlock()
	if exists {
		return nil
	}
	task, err := h.acquireRequestTask(request.Raw())
	if err != nil {
		return err
	}
	h.rememberRequestTask(request, task, false)
	return nil
}

// waitApplicationRequestTasks 让监听宿主退出前等待已返回 ServeHTTP 的后台清理。
func (h *Http) waitApplicationRequestTasks() error {
	timeout := h.srvConf.ShutdownTimeout
	if timeout <= 0 {
		timeout = time.Second
	}
	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), timeout)
	defer cancel()
	var waitErr error
	for _, handler := range h.applicationLifecycleHandlers() {
		if err := handler.app.WaitRequestTasks(ctx); err != nil {
			waitErr = errors.Join(waitErr, err)
		}
	}
	return waitErr
}
