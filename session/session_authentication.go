package session

// SetAuthenticated publishes an application-verified identity or privilege value
// only after rotating the request's session ID and revoking its persisted old ID.
// The caller must verify credentials and authorization before invoking it; the
// method does not authenticate the value or assign meaning to the key.
//
// Input validation occurs before rotation. A rotation failure leaves the value
// unchanged. As with Set, success still requires Save or the Session middleware
// before a successful response; a persistence failure must fail the login.
// Existing unrelated data is retained. If the merged session exceeds its size
// limit, publication can fail after the old ID has already been revoked.
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
	if err := s.regenerateLocked(); err != nil {
		return err
	}
	return s.setEncoded(name, encoded)
}
