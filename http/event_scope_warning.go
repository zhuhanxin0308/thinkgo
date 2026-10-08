package http

import (
	"sync"

	"github.com/zhuhanxin0308/thinkgo/v3/event"
	frameworklog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

const applicationRunWarningMessage = "应用级 HttpRun 监听器不会在请求开始阶段执行；请使用项目级监听器或全局中间件"

// applicationRunWarningState 仅保留最近已告警的服务快照，不持有无界历史。
// 服务发布仍由 HTTP 初始化/运行租约串行化；此锁只负责并发诊断去重。
type applicationRunWarningState struct {
	mu         sync.Mutex
	dispatcher *event.Dispatcher
	logger     *frameworklog.Log
}

func (state *applicationRunWarningState) claim(dispatcher *event.Dispatcher, logger *frameworklog.Log) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.dispatcher == dispatcher && state.logger == logger {
		return false
	}
	state.dispatcher, state.logger = dispatcher, logger
	return true
}

// warnApplicationRunListeners 对连续相同的事件/日志快照去重。
// Boot 后的快照不能消耗最终运行快照的告警机会；合法重绑定后重新诊断。
// 不在请求热路径扫描注册表，也不提前执行应用回调。
func (h *Http) warnApplicationRunListeners() {
	if h == nil {
		return
	}
	dispatcher, logger := h.event, h.log
	if dispatcher == nil || logger == nil || !dispatcher.HasApplicationListener(event.EventHttpRun) {
		return
	}
	if !h.httpRunWarning.claim(dispatcher, logger) {
		return
	}
	// 日志器可能执行自定义驱动；不在去重锁内调用外部代码。
	logger.WarningCtx(applicationRunWarningMessage, map[string]interface{}{
		"event": event.EventHttpRun,
		"scope": "application",
		"phase": "before_application_resolution",
	})
}
