package session

import "encoding/json"

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
	oldID, err := s.rotateSessionLocked(func(merged map[string]json.RawMessage) {
		merged[name] = append(json.RawMessage(nil), encoded...)
	})
	return s.reportIDError("认证 Session 轮换失败", err, oldID)
}
