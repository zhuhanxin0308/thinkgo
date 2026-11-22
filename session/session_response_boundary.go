package session

import (
	"errors"
	"net/http"
	"slices"
)

// corruptSessionCookieError 保留失败原因，并允许错误响应按原作用域删除对应 Cookie。
type corruptSessionCookieError struct {
	cause   error
	session *Session
}

func (e *corruptSessionCookieError) Error() string { return e.cause.Error() }
func (e *corruptSessionCookieError) Unwrap() error { return e.cause }

func (e *corruptSessionCookieError) ClearSessionCookie(writer http.ResponseWriter) error {
	e.session.saveMu.Lock()
	defer e.session.saveMu.Unlock()
	if err := e.session.SetResponseWriter(writer); err != nil {
		return err
	}
	return e.session.writeSessionCookie("", -1)
}

// saveLocked 在调用者持有 saveMu 时稳定持久化请求级 Session 快照。
func (s *Session) saveLocked() error {
	for pass := 0; pass < maxSaveStabilizationPasses; pass++ {
		snapshot := s.snapshotForSave()
		if snapshot.Destroyed {
			return s.flushCookie(snapshot.ID, true)
		}
		if !s.dirtySnapshot(snapshot) {
			if snapshot.CookieDirty {
				return s.flushCookie(snapshot.ID, false)
			}
			if snapshot.InvalidCookie {
				return s.flushCookie(snapshot.ID, true)
			}
			return nil
		}

		merged, err := s.persistSnapshot(snapshot)
		if errors.Is(err, ErrSessionIDCollision) || errors.Is(err, errSessionRecordMissing) {
			if rotateErr := s.rotateMissingID(snapshot.ID); rotateErr != nil {
				return rotateErr
			}
			continue
		}
		if err != nil {
			return s.reportIDError("Session 原子持久化失败", err, snapshot.ID)
		}
		s.reconcilePersistedSnapshot(snapshot, merged)
		if err = s.flushCookie(snapshot.ID, false); err != nil {
			return err
		}
		if !s.hasDirtyState() {
			return nil
		}
	}
	return ErrSessionBusy
}

// CommitResponse 原子冻结变更入口并持久化最终快照，保证响应提交后不会静默丢失新 mutation。
func (s *Session) CommitResponse() error {
	if !s.isRequestSession() {
		return ErrInvalidSessionDependency
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	s.mu.Lock()
	if s.responseCommitted {
		err := s.responseCommitErr
		s.mu.Unlock()
		return err
	}
	s.responseSealing = true
	writer := s.responseWriter
	s.mu.Unlock()

	err := s.saveLocked()
	if err == nil && len(s.issuedCookies) > 1 && !isNilSessionDependency(writer) {
		keepFinalSessionCookie(writer.Header(), s.issuedCookies)
	}
	s.issuedCookies = nil
	s.mu.Lock()
	s.responseSealing = false
	s.responseCommitted = true
	s.responseCommitErr = err
	s.mu.Unlock()
	return err
}

// keepFinalSessionCookie 只撤销本次请求实际写出的旧阶段头，业务头的属性、内容和顺序均原样保留。
func keepFinalSessionCookie(header http.Header, issued []string) {
	values := header.Values("Set-Cookie")
	latest := issued[len(issued)-1]
	if !slices.Contains(values, latest) {
		return
	}
	// 从后向前匹配写入次数；多次保存可能在同一秒生成完全相同的签名头。
	remaining := make(map[string]int, len(issued))
	for _, value := range issued {
		remaining[value]++
	}
	keptFinal := false
	start := len(values)
	for index := len(values) - 1; index >= 0; index-- {
		value := values[index]
		if remaining[value] > 0 {
			remaining[value]--
			if value != latest || keptFinal {
				continue
			}
			keptFinal = true
		}
		start--
		values[start] = value
	}
	clear(values[:start])
	header["Set-Cookie"] = values[start:]
}
