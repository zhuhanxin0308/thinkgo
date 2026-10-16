package session

import (
	"encoding/json"
	"time"
)

// SetAuthenticated publishes an application-verified identity or privilege value
// only after rotating the request's session ID and revoking its persisted old ID.
// The caller must verify credentials and authorization before invoking it; the
// method does not authenticate the value or assign meaning to the key.
//
// Input validation occurs before rotation. A rotation failure leaves the value
// unchanged. As with Set, success still requires Save or the Session middleware
// before a successful response; a persistence failure must fail the login.
// Existing unrelated data is retained. The complete merged session is checked
// inside the driver's atomic update before revoking the old ID; a size rejection
// leaves the old ID, its persisted data and the request's state unchanged.
func (s *Session) SetAuthenticated(name string, value interface{}) error {
	if !s.isRequestSession() {
		return ErrInvalidSessionDependency
	}
	encoded, err := s.encodeSetValue(name, value)
	if err != nil {
		return err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	var oldID string
	err = func() error {
		// Freeze request mutations through preflight, revocation and publication.
		// Otherwise a concurrent Set could invalidate the checked byte budget.
		s.mu.Lock()
		defer s.mu.Unlock()
		oldID = s.id
		if s.responseSealing || s.responseCommitted {
			return ErrSessionCommitted
		}
		if s.destroyed {
			return ErrSessionDestroyed
		}
		if s.destroying {
			return ErrSessionBusy
		}
		newID, err := newSessionID()
		if err != nil {
			return err
		}
		snapshot := saveSnapshot{Cleared: s.cleared, Mutations: s.mutations}
		prepare := func(base map[string]json.RawMessage, now time.Time) (map[string]json.RawMessage, error) {
			merged := applySessionSnapshot(base, snapshot)
			merged[name] = append(json.RawMessage(nil), encoded...)
			candidate := sessionEnvelope{Version: sessionEnvelopeVersion, Data: merged}
			if s.config.Expire > 0 {
				candidate.ExpireAt = now.Add(time.Duration(s.config.Expire) * time.Second).Unix()
			}
			// Encode the actual envelope: a driver's valid RawMessage may still
			// expand when encoding/json escapes HTML characters during Save.
			content, _, err := encodeSessionEnvelope(candidate, s.config.MaxDataBytes)
			if err != nil {
				return nil, err
			}
			// Install the same representation that Save will encode, so a Set
			// waiting on mu checks the canonical size rather than raw input size.
			canonical, err := decodeSessionEnvelope(content, s.config.MaxDataBytes)
			if err != nil {
				return nil, err
			}
			return canonical.Data, nil
		}
		var merged map[string]json.RawMessage
		if s.persisted {
			err = s.updateDriver(oldID, func(current string, found bool) (string, bool, error) {
				// Check freshness at callback time, after waiting for the driver lock.
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
			return err
		}
		s.installRegeneratedDataLocked(newID, merged)
		return nil
	}()
	return s.reportError("认证 Session 轮换失败", err, map[string]interface{}{"session_id": oldID})
}
