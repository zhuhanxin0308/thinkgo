package http

import "github.com/zhuhanxin0308/thinkgo/v3/event"

const applicationRunWarningMessage = "应用级 HttpRun 监听器不会在请求开始阶段执行；请使用项目级监听器或全局中间件"

// warnApplicationRunListeners 在 Boot 后和监听租约固定服务后检查最终事件快照。
// 每个 HTTP 内核最多记录一次，不在请求热路径扫描注册表，也不提前执行应用回调。
func (h *Http) warnApplicationRunListeners() {
	if h == nil || h.event == nil || h.log == nil || !h.event.HasApplicationListener(event.EventHttpRun) {
		return
	}
	h.httpRunWarningOnce.Do(func() {
		h.log.WarningCtx(applicationRunWarningMessage, map[string]interface{}{
			"event": event.EventHttpRun,
			"scope": "application",
			"phase": "before_application_resolution",
		})
	})
}
