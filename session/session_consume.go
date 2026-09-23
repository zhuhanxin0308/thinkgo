package session

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"time"
)

// ConsumeString 在持久化存储中原子销毁一次性字符串，避免并发请求复用旧快照。
// 无论提交值是否匹配，只要令牌存在，本次尝试都会销毁它。
func (s *Session) ConsumeString(name, submitted string) (bool, error) {
	if err := validateSessionKey(name); err != nil {
		return false, err
	}
	if !s.isRequestSession() {
		return false, ErrInvalidSessionDependency
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	sessionID := s.id
	accepted, err := s.consumeStringLocked(name, submitted)
	s.mu.Unlock()
	if err != nil {
		return false, s.reportError("消费一次性 Session 值失败", err, map[string]interface{}{"session_id": sessionID})
	}
	return accepted, nil
}

func (s *Session) consumeStringLocked(name, submitted string) (bool, error) {
	if s.responseSealing || s.responseCommitted {
		return false, ErrSessionCommitted
	}
	if s.destroyed {
		return false, ErrSessionDestroyed
	}
	if s.destroying {
		return false, ErrSessionBusy
	}
	local, exists := s.data[name]
	if !exists {
		return false, nil
	}
	localSet := false
	if mutation, changed := s.mutations[name]; changed && !mutation.Delete {
		localSet = true
	}
	accepted := false
	if !s.persisted {
		accepted = sessionStringMatches(local, submitted)
	} else {
		now := s.nowLocked()
		err := s.updateDriver(s.id, func(content string, found bool) (string, bool, error) {
			accepted = false
			if !found {
				return "", false, errSessionRecordMissing
			}
			envelope, decodeErr := decodeSessionEnvelope(content, s.config.MaxDataBytes)
			if decodeErr != nil {
				return "", false, decodeErr
			}
			if envelope.Revoked || sessionEnvelopeExpired(envelope, now) {
				return "", false, ErrSessionRevoked
			}
			latest, stored := envelope.Data[name]
			if localSet {
				accepted = sessionStringMatches(local, submitted)
			} else if stored && bytes.Equal(local, latest) {
				accepted = sessionStringMatches(latest, submitted)
			}
			if !stored {
				return content, false, nil
			}
			delete(envelope.Data, name)
			if s.config.Expire > 0 {
				envelope.ExpireAt = now.Add(time.Duration(s.config.Expire) * time.Second).Unix()
			}
			return encodeSessionEnvelope(envelope, s.config.MaxDataBytes)
		})
		if err != nil {
			return false, err
		}
	}
	s.deleteLocked(name)
	return accepted, nil
}

func sessionStringMatches(raw json.RawMessage, submitted string) bool {
	var stored string
	if err := json.Unmarshal(raw, &stored); err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(submitted)) == 1
}
