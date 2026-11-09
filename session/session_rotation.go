package session

import (
	"encoding/json"
	"time"
)

// rotateSessionLocked 要求持有 saveMu，统一普通轮换与认证轮换的提交语义。
// transform 仅供内部追加已验证的数据，不能执行外部回调。
func (s *Session) rotateSessionLocked(transform func(map[string]json.RawMessage)) (string, error) {
	// 冻结同请求增量直至预检、撤销和发布完成，防止并发 Set 推翻已检查的字节预算。
	s.mu.Lock()
	defer s.mu.Unlock()
	oldID := s.id
	if s.responseSealing || s.responseCommitted {
		return oldID, ErrSessionCommitted
	}
	if s.destroyed {
		return oldID, ErrSessionDestroyed
	}
	if s.destroying {
		return oldID, ErrSessionBusy
	}
	newID, err := newSessionID()
	if err != nil {
		return oldID, err
	}
	snapshot := saveSnapshot{Cleared: s.cleared, Mutations: s.mutations}
	prepare := func(base map[string]json.RawMessage, now time.Time) (map[string]json.RawMessage, error) {
		merged := applySessionSnapshot(base, snapshot)
		if transform != nil {
			transform(merged)
		}
		candidate := sessionEnvelope{Version: sessionEnvelopeVersion, Data: merged}
		if s.config.Expire > 0 {
			candidate.ExpireAt = now.Add(time.Duration(s.config.Expire) * time.Second).Unix()
		}
		// 真实编码包含过期字段和 HTML 转义，不能只按输入 RawMessage 长度估算。
		content, _, err := encodeSessionEnvelope(candidate, s.config.MaxDataBytes)
		if err != nil {
			return nil, err
		}
		// 发布 Save 将采用的规范表示，使等待中的 Set 继续按相同预算判断。
		canonical, err := decodeSessionEnvelope(content, s.config.MaxDataBytes)
		if err != nil {
			return nil, err
		}
		return canonical.Data, nil
	}
	var merged map[string]json.RawMessage
	if s.persisted {
		err = s.updateDriver(oldID, func(current string, found bool) (string, bool, error) {
			// 在驱动锁等待结束后重新取时间，且在同一次原子回调内预检和撤销。
			now := s.nowLocked()
			base := make(map[string]json.RawMessage)
			if found {
				envelope, err := decodeSessionEnvelope(current, s.config.MaxDataBytes)
				if err != nil {
					return "", false, err
				}
				if envelope.Revoked || sessionEnvelopeExpired(envelope, now) {
					return "", false, ErrSessionRevoked
				}
				base = envelope.Data
			}
			var err error
			merged, err = prepare(base, now)
			if err != nil {
				return "", false, err
			}
			return encodeRevokedEnvelope(s.config, now)
		})
	} else {
		merged, err = prepare(s.data, s.nowLocked())
	}
	if err != nil {
		return oldID, err
	}
	s.installRegeneratedDataLocked(newID, merged)
	return oldID, nil
}
