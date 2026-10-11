package http

import (
	"strings"
	"sync"

	frameworklog "github.com/zhuhanxin0308/thinkgo/v3/log"
)

const productionRateLimitWarningMessage = "生产环境未启用内置 rate_limit 配置；请确认网关或敏感路由已配置合适的限流策略"

// productionRateLimitWarningState retains only the latest warned logger. The
// final runtime logger must not lose a warning consumed by an earlier logger.
type productionRateLimitWarningState struct {
	mu     sync.Mutex
	logger *frameworklog.Log
}

func (state *productionRateLimitWarningState) claim(logger *frameworklog.Log) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.logger == logger {
		return false
	}
	state.logger = logger
	return true
}

// warnProductionRateLimit is advisory only: it does not install middleware,
// change a limit, or record a startup error. Configuration alone cannot attest
// to protection by a gateway, a route-specific limiter or a replaced pipeline.
func (h *Http) warnProductionRateLimit() {
	if h == nil || h.app == nil || h.log == nil || h.app.Config() == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(h.app.Environment())) {
	case "prod", "production", "release":
	default:
		return
	}
	if raw, exists := h.app.Config().GetMap("rate_limit")["enable"]; exists {
		enabled, valid := raw.(bool)
		if !valid || enabled {
			// Invalid configuration is handled by the existing strict startup
			// parser, not misclassified as an ordinary disabled policy here.
			return
		}
	}
	logger := h.log
	if !h.rateLimitWarning.claim(logger) {
		return
	}
	// Never call user-controlled logging code while holding the claim lock.
	logger.WarningCtx(productionRateLimitWarningMessage, map[string]interface{}{
		"code": "rate_limit_disabled", "configuration": "rate_limit.enable",
		"scope": "http", "phase": "startup",
	})
}
