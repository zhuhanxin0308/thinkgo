package session

import "errors"

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
			return s.reportError("Session 原子持久化失败", err, map[string]interface{}{"session_id": snapshot.ID})
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
	s.mu.Unlock()

	err := s.saveLocked()
	s.mu.Lock()
	s.responseSealing = false
	s.responseCommitted = true
	s.responseCommitErr = err
	s.mu.Unlock()
	return err
}
