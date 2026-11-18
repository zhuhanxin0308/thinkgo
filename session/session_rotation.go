package session

import (
	"encoding/json"
	"time"
)

// waitRotationLocked 要求持有 s.mu.Lock()，在锁外等待进行中的会话轮换完成，
// 避免并发写入在轮换 I/O 期间挂起读锁或绕过规范化字节预算。
func (s *Session) waitRotationLocked() {
	for s.rotating != nil {
		wait := s.rotating
		s.mu.Unlock()
		<-wait
		s.mu.Lock()
	}
}

// rotateSessionLocked 要求持有 saveMu，统一普通轮换与认证轮换的提交语义。
// transform 仅供内部追加已验证的数据，不能执行外部回调。
func (s *Session) rotateSessionLocked(transform func(map[string]json.RawMessage)) (string, error) {
	// 仅在内存锁内抓取不可变快照并登记轮换屏障，驱动 I/O 与编解码均在 s.mu 之外执行，
	// 保证并发读取无阻塞，同时串行化同请求写入以维护规范化字节预算。
	s.mu.Lock()
	s.waitRotationLocked()
	oldID := s.id
	if s.responseSealing || s.responseCommitted {
		s.mu.Unlock()
		return oldID, ErrSessionCommitted
	}
	if s.destroyed {
		s.mu.Unlock()
		return oldID, ErrSessionDestroyed
	}
	if s.destroying {
		s.mu.Unlock()
		return oldID, ErrSessionBusy
	}
	persisted := s.persisted
	snapshot := saveSnapshot{Cleared: s.cleared, Mutations: cloneSessionMutations(s.mutations)}
	var localData map[string]json.RawMessage
	if !persisted {
		localData = cloneSessionData(s.data)
	}
	rotating := make(chan struct{})
	s.rotating = rotating
	s.mu.Unlock()

	finished := false
	defer func() {
		if !finished {
			s.mu.Lock()
			if s.rotating == rotating {
				s.rotating = nil
			}
			close(rotating)
			s.mu.Unlock()
		}
	}()

	newID, err := newSessionID()
	if err != nil {
		return oldID, err
	}
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
	if persisted {
		err = s.updateDriver(oldID, func(current string, found bool) (string, bool, error) {
			// 在驱动锁等待结束后重新取时间，且在同一次原子回调内预检和撤销。
			now := s.nowTime()
			base := make(map[string]json.RawMessage)
			if found {
				envelope, decodeErr := decodeSessionEnvelope(current, s.config.MaxDataBytes)
				if decodeErr != nil {
					return "", false, decodeErr
				}
				if envelope.Revoked || sessionEnvelopeExpired(envelope, now) {
					return "", false, ErrSessionRevoked
				}
				base = envelope.Data
			}
			prepared, prepErr := prepare(base, now)
			if prepErr != nil {
				return "", false, prepErr
			}
			revoked, remove, revErr := encodeRevokedEnvelope(s.config, now)
			if revErr != nil {
				return "", false, revErr
			}
			merged = prepared
			return revoked, remove, nil
		})
	} else {
		merged, err = prepare(localData, s.nowTime())
	}

	s.mu.Lock()
	if err == nil {
		s.installRegeneratedDataLocked(newID, merged)
	}
	if s.rotating == rotating {
		s.rotating = nil
	}
	close(rotating)
	finished = true
	s.mu.Unlock()
	if err != nil {
		return oldID, err
	}
	return oldID, nil
}
