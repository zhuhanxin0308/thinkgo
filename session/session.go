package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
)

const (
	// DefaultSessionName 是默认 Session Cookie 名称。
	DefaultSessionName         = "PHPSESSID"
	sessionEnvelopeVersion     = 1
	maxSaveStabilizationPasses = 16
)

var (
	errSessionRecordMissing = errors.New("Session 记录已消失")
	// ErrSessionBusy 表示同一请求在 Save 期间持续修改，无法得到稳定快照。
	ErrSessionBusy = errors.New("Session 持续并发修改")
)

// Logger 抽象 Session 所需的最小错误日志能力。
type Logger interface {
	ErrorCtx(msg string, ctx map[string]interface{})
}

// Session 同时承载不可变管理器配置与隔离的请求级状态。
type Session struct {
	mu     sync.RWMutex
	saveMu sync.Mutex

	config         Config
	driver         Driver
	logger         Logger
	cookie         *cookie.Cookie
	now            func() time.Time
	requestContext context.Context

	requestState      bool
	id                string
	data              map[string]json.RawMessage
	dataJSONBytes     int
	mutations         map[string]sessionMutation
	cleared           bool
	clearVersion      uint64
	version           uint64
	dirty             bool
	cookieDirty       bool
	invalidCookie     bool
	destroying        bool
	destroyed         bool
	persisted         bool
	responseSealing   bool
	responseCommitted bool
	responseCommitErr error
}

type sessionMutation struct {
	Value   json.RawMessage
	Delete  bool
	Version uint64
}

// sessionEnvelope 是唯一受支持的持久化格式；撤销墓碑不携带业务数据。
type sessionEnvelope struct {
	Version  int                        `json:"version"`
	ExpireAt int64                      `json:"expire_at,omitempty"`
	Revoked  bool                       `json:"revoked,omitempty"`
	Data     map[string]json.RawMessage `json:"data"`
}

type saveSnapshot struct {
	ID            string
	Persisted     bool
	Version       uint64
	Cleared       bool
	Mutations     map[string]sessionMutation
	CookieDirty   bool
	InvalidCookie bool
	Destroyed     bool
}

// NewSession 严格解析配置并创建 Session 管理器。
func NewSession(rawConfig map[string]interface{}, driver Driver, cookieFactory *cookie.Cookie) (*Session, error) {
	config, err := ParseConfig(rawConfig)
	if err != nil {
		return nil, err
	}
	return NewSessionWithConfig(config, driver, cookieFactory)
}

// NewSessionWithConfig 使用已解析配置创建 Session 管理器。
func NewSessionWithConfig(config Config, driver Driver, cookieFactory *cookie.Cookie) (*Session, error) {
	if err := validateSessionConfig(config); err != nil {
		return nil, err
	}
	if isNilSessionDependency(driver) || cookieFactory == nil {
		return nil, ErrInvalidSessionDependency
	}
	return &Session{
		config:         config,
		driver:         driver,
		cookie:         cookieFactory,
		now:            time.Now,
		requestContext: context.Background(),
	}, nil
}

// GetConfig 返回请求无法修改的配置值副本。
func (s *Session) GetConfig() Config {
	if s == nil {
		return Config{}
	}
	return s.config
}

// SetLogger 设置错误日志记录器；新请求会复制设置时的记录器引用。
func (s *Session) SetLogger(logger Logger) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.logger = logger
	s.mu.Unlock()
}

// Close 关闭底层 Session 驱动；不支持关闭的进程内驱动保持幂等空操作。
func (s *Session) Close() error {
	if s == nil || isNilSessionDependency(s.driver) {
		return nil
	}
	if closer, ok := s.driver.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

// NewRequestSession 创建请求级 Session，并显式传播 Cookie、存储和解码错误。
func (s *Session) NewRequestSession(req *http.Request, writer http.ResponseWriter) (*Session, error) {
	return s.newRequestSession(req, writer, "", false, false)
}

// NewRequestSessionWithSecure 创建请求级 Session，并使用调用方已经完成可信代理校验的协议结论。
func (s *Session) NewRequestSessionWithSecure(req *http.Request, writer http.ResponseWriter, secure bool) (*Session, error) {
	return s.newRequestSession(req, writer, "", secure, true)
}

// NewRequestSessionWithIDAndSecure 使用 var_session_id 提交的 ID，并使用调用方
// 已完成可信代理校验的协议结论。
func (s *Session) NewRequestSessionWithIDAndSecure(req *http.Request, writer http.ResponseWriter, id string, secure bool) (*Session, error) {
	return s.newRequestSession(req, writer, id, secure, true)
}

func (s *Session) newRequestSession(req *http.Request, writer http.ResponseWriter, requestedID string, secure, secureSet bool) (*Session, error) {
	if s == nil || req == nil || isNilSessionDependency(s.driver) || s.cookie == nil {
		return nil, ErrInvalidSessionDependency
	}
	s.mu.RLock()
	logger := s.logger
	now := s.now
	s.mu.RUnlock()
	if now == nil {
		now = time.Now
	}
	var requestCookie *cookie.Cookie
	var err error
	if secureSet {
		requestCookie, err = s.cookie.ForRequestWithSecure(req, writer, secure)
	} else {
		requestCookie, err = s.cookie.ForRequest(req, writer)
	}
	if err != nil {
		return nil, errors.Join(ErrSessionCookie, err)
	}
	requestSession := &Session{
		config:         s.config,
		driver:         s.driver,
		logger:         logger,
		cookie:         requestCookie,
		now:            now,
		requestContext: requestContextForHTTP(req),
		requestState:   true,
		data:           make(map[string]json.RawMessage),
		dataJSONBytes:  2,
		mutations:      make(map[string]sessionMutation),
	}
	if err = requestSession.bootstrap(requestedID); err != nil {
		return nil, err
	}
	return requestSession, nil
}

// SetResponseWriter 为请求级 Session 绑定响应 writer。
func (s *Session) SetResponseWriter(writer http.ResponseWriter) error {
	if !s.isRequestSession() {
		return ErrInvalidSessionDependency
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.responseSealing || s.responseCommitted {
		return ErrSessionCommitted
	}
	if err := s.cookie.SetWriter(writer); err != nil {
		return errors.Join(ErrSessionCookie, err)
	}
	return nil
}

// Set 校验、编码并隔离存储值；失败时不修改请求状态。
func (s *Session) Set(name string, value interface{}) error {
	if err := validateSessionKey(name); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSessionValue, err)
	}
	if len(encoded) > s.config.MaxDataBytes {
		return fmt.Errorf("%w: %d", ErrSessionDataTooLarge, len(encoded))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.requestState {
		return ErrInvalidSessionDependency
	}
	if s.responseSealing || s.responseCommitted {
		return ErrSessionCommitted
	}
	if s.destroyed {
		return ErrSessionDestroyed
	}
	if s.destroying {
		return ErrSessionBusy
	}
	previous, existed := s.data[name]
	if existed && bytes.Equal(previous, encoded) && s.config.Expire <= 0 {
		return nil
	}
	nextDataBytes := s.dataJSONBytes
	newEntryBytes := sessionJSONKeyBytes(name) + 1 + len(encoded)
	if existed {
		oldEntryBytes := sessionJSONKeyBytes(name) + 1 + len(previous)
		nextDataBytes += newEntryBytes - oldEntryBytes
	} else if len(s.data) == 0 {
		nextDataBytes += newEntryBytes
	} else {
		nextDataBytes += 1 + newEntryBytes
	}
	if envelopeBytes := activeSessionEnvelopeBytes(nextDataBytes, s.config, s.nowLocked()); envelopeBytes > s.config.MaxDataBytes {
		return fmt.Errorf("%w: %d", ErrSessionDataTooLarge, envelopeBytes)
	}
	s.data[name] = append(json.RawMessage(nil), encoded...)
	s.dataJSONBytes = nextDataBytes
	s.version++
	s.mutations[name] = sessionMutation{
		Value: append(json.RawMessage(nil), encoded...), Version: s.version,
	}
	s.dirty = true
	return nil
}

// Get 解码一份隔离的 JSON 值，并区分缺失与显式 null。
func (s *Session) Get(name string) (interface{}, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	raw, found := s.data[name]
	copyRaw := append(json.RawMessage(nil), raw...)
	s.mu.RUnlock()
	if !found {
		return nil, false
	}
	value, err := decodeSessionValue(copyRaw)
	if err != nil {
		return nil, false
	}
	return value, true
}

// All 返回当前 Session 数据的隔离快照。
func (s *Session) All() map[string]interface{} {
	result := make(map[string]interface{})
	if s == nil {
		return result
	}
	s.mu.RLock()
	snapshot := cloneSessionData(s.data)
	s.mu.RUnlock()
	for name, raw := range snapshot {
		value, err := decodeSessionValue(raw)
		if err == nil {
			result[name] = value
		}
	}
	return result
}

// Has 判断 Session 键是否存在；显式 null 仍视为存在。
func (s *Session) Has(name string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, found := s.data[name]
	return found
}

// Delete 删除单个键，并记录可原子合并的删除增量。
func (s *Session) Delete(name string) error {
	if err := validateSessionKey(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.requestState {
		return ErrInvalidSessionDependency
	}
	if s.responseSealing || s.responseCommitted {
		return ErrSessionCommitted
	}
	if s.destroyed {
		return ErrSessionDestroyed
	}
	if s.destroying {
		return ErrSessionBusy
	}
	s.version++
	if previous, found := s.data[name]; found {
		entryBytes := sessionJSONKeyBytes(name) + 1 + len(previous)
		if len(s.data) == 1 {
			s.dataJSONBytes = 2
		} else {
			s.dataJSONBytes -= entryBytes + 1
		}
		delete(s.data, name)
	}
	s.mutations[name] = sessionMutation{Delete: true, Version: s.version}
	s.dirty = true
	return nil
}

// Clear 原子清除保存时后端的最新状态，并允许随后 Set 新值。
func (s *Session) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.requestState {
		return ErrInvalidSessionDependency
	}
	if s.responseSealing || s.responseCommitted {
		return ErrSessionCommitted
	}
	if s.destroyed {
		return ErrSessionDestroyed
	}
	if s.destroying {
		return ErrSessionBusy
	}
	s.version++
	s.data = make(map[string]json.RawMessage)
	s.dataJSONBytes = 2
	s.mutations = make(map[string]sessionMutation)
	s.cleared = true
	s.clearVersion = s.version
	s.dirty = true
	return nil
}

// Save 使用驱动原子 Update 合并增量，先持久化成功后再写客户端 Cookie。
func (s *Session) Save() error {
	if !s.isRequestSession() {
		return ErrInvalidSessionDependency
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.RLock()
	committed := s.responseSealing || s.responseCommitted
	s.mu.RUnlock()
	if committed {
		return ErrSessionCommitted
	}
	return s.saveLocked()
}

// Regenerate 原子撤销旧 ID，并把后端最新数据与本请求增量迁移到新随机 ID。
func (s *Session) Regenerate() error {
	if !s.isRequestSession() {
		return ErrInvalidSessionDependency
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.RLock()
	committed := s.responseSealing || s.responseCommitted
	s.mu.RUnlock()
	if committed {
		return ErrSessionCommitted
	}
	newID, err := newSessionID()
	if err != nil {
		return s.reportError("生成 Session ID 失败", err, nil)
	}
	snapshot := s.snapshotForSave()
	if snapshot.Destroyed {
		return ErrSessionDestroyed
	}
	merged := s.snapshotData()
	if snapshot.Persisted {
		err = s.updateDriver(snapshot.ID, func(current string, found bool) (string, bool, error) {
			base := make(map[string]json.RawMessage)
			if found {
				envelope, decodeErr := decodeSessionEnvelope(current, s.config.MaxDataBytes)
				if decodeErr != nil {
					return "", false, decodeErr
				}
				if envelope.Revoked || sessionEnvelopeExpired(envelope, s.nowTime()) {
					return "", false, ErrSessionRevoked
				}
				base = cloneSessionData(envelope.Data)
			}
			base = applySessionSnapshot(base, snapshot)
			merged = base
			return encodeRevokedEnvelope(s.config, s.nowTime())
		})
		if err != nil {
			return s.reportError("撤销旧 Session ID 失败", err, map[string]interface{}{"session_id": snapshot.ID})
		}
	}
	s.mu.Lock()
	merged = s.applyMutationsAfterVersionLocked(merged, snapshot.Version)
	s.id = newID
	s.persisted = false
	s.invalidCookie = false
	s.destroyed = false
	s.data = cloneSessionData(merged)
	s.dataJSONBytes = sessionDataJSONSize(merged)
	s.version++
	s.cleared = true
	s.clearVersion = s.version
	s.mutations = make(map[string]sessionMutation, len(merged))
	for key, value := range merged {
		s.mutations[key] = sessionMutation{Value: append(json.RawMessage(nil), value...), Version: s.version}
	}
	s.dirty = true
	s.cookieDirty = true
	s.mu.Unlock()
	return nil
}

// Destroy 原子写入撤销墓碑；只有成功后才改变本地状态，Cookie 删除由 Save 完成。
func (s *Session) Destroy() error {
	if !s.isRequestSession() {
		return ErrInvalidSessionDependency
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	if s.responseSealing || s.responseCommitted {
		s.mu.Unlock()
		return ErrSessionCommitted
	}
	if s.destroyed {
		s.mu.Unlock()
		return nil
	}
	s.destroying = true
	s.mu.Unlock()
	snapshot := s.snapshotForSave()
	if snapshot.Persisted {
		err := s.updateDriver(snapshot.ID, func(current string, found bool) (string, bool, error) {
			if found {
				envelope, decodeErr := decodeSessionEnvelope(current, s.config.MaxDataBytes)
				if decodeErr != nil {
					return "", false, decodeErr
				}
				if envelope.Revoked {
					return current, false, nil
				}
			}
			return encodeRevokedEnvelope(s.config, s.nowTime())
		})
		if err != nil {
			s.mu.Lock()
			s.destroying = false
			s.mu.Unlock()
			return s.reportError("撤销 Session 失败", err, map[string]interface{}{"session_id": snapshot.ID})
		}
	}
	s.mu.Lock()
	s.data = make(map[string]json.RawMessage)
	s.dataJSONBytes = 2
	s.mutations = make(map[string]sessionMutation)
	s.cleared = false
	s.dirty = false
	s.invalidCookie = false
	s.destroying = false
	s.destroyed = true
	s.cookieDirty = true
	s.version++
	s.mu.Unlock()
	return nil
}

// GC 触发驱动回收，expire=0 时按 24 小时清理陈旧文件。
func (s *Session) GC() (int, error) {
	if s == nil || isNilSessionDependency(s.driver) {
		return 0, ErrInvalidSessionDependency
	}
	collector, supported := s.driver.(GarbageCollector)
	if !supported {
		return 0, nil
	}
	lifetime := time.Duration(s.config.Expire) * time.Second
	if lifetime <= 0 {
		lifetime = 24 * time.Hour
	}
	return collector.GC(lifetime)
}

// StartGarbageCollector 启动可等待退出的回收循环；stop 返回时协程已完全结束。
func (s *Session) StartGarbageCollector(interval time.Duration) (stop func()) {
	if s == nil {
		return func() {}
	}
	if _, supported := s.driver.(GarbageCollector); !supported {
		return func() {}
	}
	if interval <= 0 {
		interval = time.Hour
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := s.GC(); err != nil {
					// 后台回收没有调用方可返回错误，统一交给 Session 日志记录。
					_ = s.reportError("Session 定时回收失败", err, nil)
				}
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-finished
	}
}

func (s *Session) bootstrap(requestedID string) error {
	if requestedID != "" {
		return s.bootstrapID(requestedID, true)
	}
	id, found, err := s.cookie.Get(s.config.Name)
	if err != nil {
		if errors.Is(err, cookie.ErrInvalidCookieSignature) || errors.Is(err, cookie.ErrExpiredCookieSignature) {
			s.invalidCookie = true
			return s.assignFreshID()
		}
		return s.reportError("读取 Session Cookie 失败", errors.Join(ErrSessionCookie, err), nil)
	}
	if !found {
		return s.assignFreshID()
	}
	return s.bootstrapID(id, true)
}

func (s *Session) bootstrapID(id string, markInvalid bool) error {
	if !isSafeSessionID(id) {
		s.invalidCookie = markInvalid
		return s.assignFreshID()
	}
	content, stored, err := s.readDriver(id)
	if err != nil {
		return s.reportError("读取 Session 存储失败", err, map[string]interface{}{"session_id": id})
	}
	if !stored {
		// ThinkPHP Store 会保留合法的 32 位外部 ID，并在首次写入时创建记录。
		s.id = id
		s.persisted = false
		return nil
	}
	envelope, err := decodeSessionEnvelope(content, s.config.MaxDataBytes)
	if err != nil {
		return s.reportError("解析 Session 存储失败", err, map[string]interface{}{"session_id": id})
	}
	if envelope.Revoked || sessionEnvelopeExpired(envelope, s.nowTime()) {
		s.invalidCookie = true
		return s.assignFreshID()
	}
	s.id = id
	s.data = cloneSessionData(envelope.Data)
	s.dataJSONBytes = sessionDataJSONSize(envelope.Data)
	s.persisted = true
	return nil
}

// requestContextForHTTP 读取请求上下文，并为非 HTTP 场景提供稳定后台上下文。
func requestContextForHTTP(req *http.Request) context.Context {
	if req == nil || req.Context() == nil {
		return context.Background()
	}
	return req.Context()
}

func (s *Session) context() context.Context {
	if s == nil || s.requestContext == nil {
		return context.Background()
	}
	return s.requestContext
}

func (s *Session) readDriver(id string) (string, bool, error) {
	id = s.config.Prefix + id
	if contextual, ok := s.driver.(ContextualReader); ok {
		return contextual.ReadContext(s.context(), id)
	}
	return s.driver.Read(id)
}

func (s *Session) updateDriver(id string, update func(data string, found bool) (next string, remove bool, err error)) error {
	id = s.config.Prefix + id
	if contextual, ok := s.driver.(ContextualUpdater); ok {
		return contextual.UpdateContext(s.context(), id, update)
	}
	return s.driver.Update(id, update)
}

func (s *Session) assignFreshID() error {
	id, err := newSessionID()
	if err != nil {
		return s.reportError("生成 Session ID 失败", err, nil)
	}
	s.id = id
	s.persisted = false
	return nil
}

func (s *Session) snapshotForSave() saveSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return saveSnapshot{
		ID: s.id, Persisted: s.persisted, Version: s.version, Cleared: s.cleared,
		Mutations: cloneSessionMutations(s.mutations), CookieDirty: s.cookieDirty,
		InvalidCookie: s.invalidCookie, Destroyed: s.destroyed,
	}
}

func (s *Session) dirtySnapshot(snapshot saveSnapshot) bool {
	return snapshot.Cleared || len(snapshot.Mutations) > 0
}

func (s *Session) persistSnapshot(snapshot saveSnapshot) (map[string]json.RawMessage, error) {
	var merged map[string]json.RawMessage
	err := s.updateDriver(snapshot.ID, func(current string, found bool) (string, bool, error) {
		if !snapshot.Persisted && found {
			return "", false, ErrSessionIDCollision
		}
		if snapshot.Persisted && !found {
			return "", false, errSessionRecordMissing
		}
		base := make(map[string]json.RawMessage)
		if found {
			envelope, err := decodeSessionEnvelope(current, s.config.MaxDataBytes)
			if err != nil {
				return "", false, err
			}
			if envelope.Revoked || sessionEnvelopeExpired(envelope, s.nowTime()) {
				return "", false, ErrSessionRevoked
			}
			base = cloneSessionData(envelope.Data)
		}
		base = applySessionSnapshot(base, snapshot)
		merged = cloneSessionData(base)
		envelope := sessionEnvelope{Version: sessionEnvelopeVersion, Data: base}
		if s.config.Expire > 0 {
			envelope.ExpireAt = s.nowTime().Add(time.Duration(s.config.Expire) * time.Second).Unix()
		}
		return encodeSessionEnvelope(envelope, s.config.MaxDataBytes)
	})
	return merged, err
}

func (s *Session) reconcilePersistedSnapshot(snapshot saveSnapshot, merged map[string]json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.id != snapshot.ID || s.destroyed {
		return
	}
	merged = s.applyMutationsAfterVersionLocked(merged, snapshot.Version)
	s.data = cloneSessionData(merged)
	s.dataJSONBytes = sessionDataJSONSize(merged)
	for key, mutation := range s.mutations {
		if mutation.Version <= snapshot.Version {
			delete(s.mutations, key)
		}
	}
	if s.cleared && s.clearVersion <= snapshot.Version {
		s.cleared = false
		s.clearVersion = 0
	}
	s.persisted = true
	s.invalidCookie = false
	s.dirty = s.cleared || len(s.mutations) > 0
	s.cookieDirty = true
}

func (s *Session) applyMutationsAfterVersionLocked(base map[string]json.RawMessage, version uint64) map[string]json.RawMessage {
	result := cloneSessionData(base)
	if s.cleared && s.clearVersion > version {
		result = make(map[string]json.RawMessage)
	}
	for key, mutation := range s.mutations {
		if mutation.Version <= version {
			continue
		}
		if mutation.Delete {
			delete(result, key)
		} else {
			result[key] = append(json.RawMessage(nil), mutation.Value...)
		}
	}
	return result
}

func (s *Session) rotateMissingID(expected string) error {
	newID, err := newSessionID()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.id != expected {
		return nil
	}
	s.id = newID
	s.persisted = false
	s.cookieDirty = true
	return nil
}

func (s *Session) flushCookie(id string, remove bool) error {
	if id == "" {
		return ErrSessionCookie
	}
	options := cookie.CookieOptions{
		Path: s.config.CookiePath, Domain: s.config.Domain, Secure: s.config.Secure,
		HttpOnly: true, SameSite: s.config.SameSite, Expire: s.config.Expire,
	}
	if remove {
		options.Expire = -1
	}
	value := id
	if remove {
		value = ""
	}
	if err := s.cookie.Set(s.config.Name, value, options); err != nil {
		return s.reportError("写入 Session Cookie 失败", errors.Join(ErrSessionCookie, err), nil)
	}
	s.mu.Lock()
	if s.id == id {
		s.cookieDirty = false
		s.invalidCookie = false
	}
	s.mu.Unlock()
	return nil
}

func (s *Session) hasDirtyState() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dirty
}

func (s *Session) snapshotData() map[string]json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSessionData(s.data)
}

func (s *Session) isRequestSession() bool {
	return s != nil && s.requestState && s.cookie != nil && !isNilSessionDependency(s.driver)
}

func (s *Session) nowTime() time.Time {
	s.mu.RLock()
	now := s.now
	s.mu.RUnlock()
	if now == nil {
		return time.Now()
	}
	return now()
}

func (s *Session) nowLocked() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

func (s *Session) reportError(message string, err error, context map[string]interface{}) error {
	if err == nil {
		return nil
	}
	s.mu.RLock()
	logger := s.logger
	s.mu.RUnlock()
	if logger != nil {
		logContext := map[string]interface{}{"component": "session"}
		for key, value := range context {
			logContext[key] = value
		}
		logger.ErrorCtx(fmt.Sprintf("%s: %v", message, err), logContext)
	}
	return err
}

func applySessionSnapshot(base map[string]json.RawMessage, snapshot saveSnapshot) map[string]json.RawMessage {
	result := cloneSessionData(base)
	if snapshot.Cleared {
		result = make(map[string]json.RawMessage)
	}
	for key, mutation := range snapshot.Mutations {
		if mutation.Delete {
			delete(result, key)
		} else {
			result[key] = append(json.RawMessage(nil), mutation.Value...)
		}
	}
	return result
}

func cloneSessionData(data map[string]json.RawMessage) map[string]json.RawMessage {
	cloned := make(map[string]json.RawMessage, len(data))
	for key, value := range data {
		cloned[key] = append(json.RawMessage(nil), value...)
	}
	return cloned
}

func cloneSessionMutations(mutations map[string]sessionMutation) map[string]sessionMutation {
	cloned := make(map[string]sessionMutation, len(mutations))
	for key, mutation := range mutations {
		mutation.Value = append(json.RawMessage(nil), mutation.Value...)
		cloned[key] = mutation
	}
	return cloned
}

func sessionEnvelopeExpired(envelope sessionEnvelope, now time.Time) bool {
	return envelope.ExpireAt > 0 && now.Unix() >= envelope.ExpireAt
}

func newSessionID() (string, error) {
	random := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return "", err
	}
	return hex.EncodeToString(random), nil
}

func isSafeSessionID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, character := range id {
		isDigit := character >= '0' && character <= '9'
		isLower := character >= 'a' && character <= 'z'
		isUpper := character >= 'A' && character <= 'Z'
		if isDigit || isLower || isUpper {
			continue
		}
		return false
	}
	return true
}

func isNilSessionDependency(value interface{}) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
