package session

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"thinkgo/framework/cookie"
)

// Logger 抽象 Session 需要的最小日志能力，避免直接依赖具体日志实现。
type Logger interface {
	ErrorCtx(msg string, ctx map[string]interface{})
}

// Session 会话管理器。
// 通过 sync.RWMutex 保护内部状态，防止同一用户的并发请求触发 map 并发写 panic。
type Session struct {
	mu        sync.RWMutex
	config    map[string]interface{}
	driver    Driver
	logger    Logger
	id        string
	data      map[string]interface{}
	cookie    *cookie.Cookie
	dirty     bool
	destroyed bool
}

// sessionEnvelope 保存服务端过期时间与真实业务数据，避免仅依赖客户端 Cookie 失效。
type sessionEnvelope struct {
	ExpireAt int64                  `json:"__expire_at,omitempty"`
	Data     map[string]interface{} `json:"__data,omitempty"`
}

// DefaultSessionName 默认会话 Cookie 名称。
const DefaultSessionName = "THINKGO_SESSID"

// NewSession 创建会话管理器，只保存全局配置与驱动。
func NewSession(config map[string]interface{}, driver Driver, cookie *cookie.Cookie) *Session {
	return &Session{
		config: config,
		driver: driver,
		cookie: cookie,
		data:   make(map[string]interface{}),
	}
}

// NewRequestSession 为每个请求创建独立的 Session 实例，避免共享状态。
func (s *Session) NewRequestSession(req *http.Request, w http.ResponseWriter) *Session {
	var cookieConfig cookie.CookieConfig
	if s.cookie != nil {
		cookieConfig = s.cookie.GetConfig()
	}
	reqCookie := cookie.NewCookieForRequest(cookieConfig, req, w)
	reqSession := &Session{
		config: s.config,
		driver: s.driver,
		logger: s.logger,
		cookie: reqCookie,
		data:   make(map[string]interface{}),
	}

	name := reqSession.getConfig("name", DefaultSessionName).(string)
	content := reqSession.bootstrap(reqCookie.Get(name))
	reqSession.loadContent(content)

	return reqSession
}

// InitCookie 初始化 Cookie 管理器，保留旧接口兼容。
func (s *Session) InitCookie(req *http.Request, w http.ResponseWriter) {
	s.cookie.Init(req, w)
}

// Init 从请求中初始化会话，保留旧接口兼容。
func (s *Session) Init(req *http.Request) {
	name := s.getConfig("name", DefaultSessionName).(string)
	content := s.bootstrap(s.cookie.Get(name))
	s.loadContent(content)
}

// SetResponseWriter 绑定响应写入器。
func (s *Session) SetResponseWriter(w http.ResponseWriter) {
	s.cookie.SetWriter(w)
}

// SetLogger 设置 Session 错误日志记录器，便于在持久化失败时留下排障证据。
func (s *Session) SetLogger(logger Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = logger
}

// Set 设置会话数据。
func (s *Session) Set(name string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[name] = value
	s.dirty = true
}

// Get 获取会话数据。
func (s *Session) Get(name string) interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[name]
}

// Has 判断会话数据是否存在。
func (s *Session) Has(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[name]
	return ok
}

// Delete 删除单个会话字段。
func (s *Session) Delete(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[name]; ok {
		delete(s.data, name)
		s.dirty = true
	}
}

// Clear 清空会话数据。
func (s *Session) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data) > 0 {
		s.data = make(map[string]interface{})
		s.dirty = true
	}
}

// Save 在必要时才落盘并写入 Cookie，避免未使用 Session 产生额外 I/O。
func (s *Session) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.destroyed {
		name := s.getConfig("name", DefaultSessionName).(string)
		s.cookie.Delete(name)
		return nil
	}

	if !s.dirty {
		return nil
	}

	expire := s.getExpireSeconds()
	envelope := sessionEnvelope{
		Data: s.data,
	}
	if expire > 0 {
		envelope.ExpireAt = time.Now().Add(time.Duration(expire) * time.Second).Unix()
	}

	content, err := json.Marshal(envelope)
	if err != nil {
		return s.reportError("session 序列化失败", err, map[string]interface{}{
			"session_id": s.id,
		})
	}
	if err := s.driver.Write(s.id, string(content)); err != nil {
		return s.reportError("session 持久化失败", err, map[string]interface{}{
			"session_id": s.id,
		})
	}

	name := s.getConfig("name", DefaultSessionName).(string)
	s.cookie.Set(name, s.id, s.buildCookieOptions(expire))
	s.dirty = false
	return nil
}

// Regenerate 轮换会话 ID，把当前数据迁移到新 ID 下并删除旧记录。
// 登录提权等敏感操作后应调用，以防会话固定攻击。
func (s *Session) Regenerate() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.destroyed {
		return nil
	}

	oldID := s.id
	s.id = uuid.New().String()
	s.dirty = true

	if oldID != "" && oldID != s.id {
		if err := s.driver.Delete(oldID); err != nil {
			return s.reportError("session 旧记录删除失败", err, map[string]interface{}{
				"old_session_id": oldID,
			})
		}
	}
	return nil
}

// GC 触发驱动层回收过期会话；驱动不支持回收时直接返回 0。
func (s *Session) GC() (int, error) {
	collector, ok := s.driver.(GarbageCollector)
	if !ok {
		return 0, nil
	}
	maxLifetime := time.Duration(s.getExpireSeconds()) * time.Second
	if maxLifetime <= 0 {
		// expire=0（会话级）时按默认 24 小时回收磁盘上的陈旧文件。
		maxLifetime = 24 * time.Hour
	}
	return collector.GC(maxLifetime)
}

// StartGarbageCollector 启动后台定时回收协程，进程退出前持续运行。
// 驱动不支持回收时为空操作。返回的 stop 函数可显式停止回收循环。
func (s *Session) StartGarbageCollector(interval time.Duration) (stop func()) {
	if _, ok := s.driver.(GarbageCollector); !ok {
		return func() {}
	}
	if interval <= 0 {
		interval = time.Hour
	}

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := s.GC(); err != nil {
					s.reportError("session 定时回收失败", err, nil)
				}
			case <-done:
				return
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
	}
}

// Destroy 销毁会话，实际删除 Cookie 延迟到 Save 阶段统一执行。
func (s *Session) Destroy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.driver.Delete(s.id); err != nil {
		s.reportError("session 删除失败", err, map[string]interface{}{
			"session_id": s.id,
		})
	}
	s.data = make(map[string]interface{})
	s.dirty = false
	s.destroyed = true
}

// reportError 统一补充 Session 上下文并记录错误，避免关键故障被静默吞掉。
func (s *Session) reportError(message string, err error, ctx map[string]interface{}) error {
	if err == nil {
		return nil
	}

	if s.logger != nil {
		logCtx := map[string]interface{}{
			"component": "session",
		}
		for key, value := range ctx {
			logCtx[key] = value
		}
		s.logger.ErrorCtx(fmt.Sprintf("%s: %v", message, err), logCtx)
	}

	return err
}

func (s *Session) getConfig(key string, defaultVal interface{}) interface{} {
	if val, ok := s.config[key]; ok {
		return val
	}
	return defaultVal
}

// getExpireSeconds 统一解析配置中的过期秒数。
func (s *Session) getExpireSeconds() int {
	expireVal := s.getConfig("expire", 0)
	if v, ok := expireVal.(int); ok {
		return v
	}
	if v, ok := expireVal.(float64); ok {
		return int(v)
	}
	return 0
}

// bootstrap 初始化安全的 Session ID，拒绝直接使用危险外部输入。
func (s *Session) bootstrap(id string) string {
	s.data = make(map[string]interface{})
	s.dirty = false
	s.destroyed = false

	if !isSafeSessionID(id) {
		id = ""
	}

	if id != "" {
		content, err := s.driver.Read(id)
		if err == nil && content != "" {
			s.id = id
			return content
		}
	}

	// 只有后端已知的会话标识才允许复用，未知 ID 必须重新生成，避免会话固定攻击。
	s.id = uuid.New().String()
	return ""
}

// loadContent 负责把已有会话内容恢复到内存状态，并在发现服务端过期时立即失效。
func (s *Session) loadContent(content string) {
	if content == "" {
		return
	}

	var envelope sessionEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err == nil && envelope.Data != nil {
		if envelope.ExpireAt > 0 && time.Now().Unix() >= envelope.ExpireAt {
			_ = s.driver.Delete(s.id)
			s.data = make(map[string]interface{})
			return
		}
		s.data = envelope.Data
		return
	}

	// 兼容历史版本直接存储 map 的旧格式。
	_ = json.Unmarshal([]byte(content), &s.data)
}

// buildCookieOptions 把 Session 自身配置映射到最终 Cookie 写入参数。
func (s *Session) buildCookieOptions(expire int) map[string]interface{} {
	options := map[string]interface{}{
		"expire":   expire,
		"httponly": true,
	}

	if value, ok := s.config["path"].(string); ok && value != "" {
		options["path"] = value
	}
	if value, ok := s.config["domain"].(string); ok {
		options["domain"] = value
	}
	if value, ok := s.config["secure"].(bool); ok {
		options["secure"] = value
	}
	if value, ok := s.config["httponly"].(bool); ok {
		options["httponly"] = value
	}
	if value, ok := s.config["samesite"].(string); ok && value != "" {
		options["samesite"] = value
	}

	return options
}

// isSafeSessionID 仅允许有限字符集，阻断路径穿越和存储键注入。
func isSafeSessionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}

	for _, char := range id {
		isDigit := char >= '0' && char <= '9'
		isLower := char >= 'a' && char <= 'z'
		isUpper := char >= 'A' && char <= 'Z'
		if isDigit || isLower || isUpper || char == '-' || char == '_' {
			continue
		}
		return false
	}

	return true
}
