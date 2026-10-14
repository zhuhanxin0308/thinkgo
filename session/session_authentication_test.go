package session

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

func authenticationBackend(t *testing.T, kind string) Driver {
	t.Helper()
	if kind == "file" {
		backend, err := driver.NewFile(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return backend
	}
	return driver.NewMemory()
}

func anonymousAuthenticationSession(t *testing.T, manager *Session) (*http.Cookie, string) {
	t.Helper()
	writer := httptest.NewRecorder()
	current, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "https://example.test/", nil), writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Set("cart", "retained"); err != nil {
		t.Fatal(err)
	}
	if err := current.Save(); err != nil {
		t.Fatal(err)
	}
	return writer.Result().Cookies()[0], current.id
}

func TestSetAuthenticatedRotatesBeforePublishingAcrossBackends(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			backend := authenticationBackend(t, kind)
			manager := newTestSessionManager(t, backend, map[string]interface{}{"reject_unknown_id": true}, nil)
			t.Cleanup(func() { _ = manager.Close() })
			oldCookie, oldID := anonymousAuthenticationSession(t, manager)
			writer := httptest.NewRecorder()
			current, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), writer)
			if err != nil {
				t.Fatal(err)
			}
			stale, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), httptest.NewRecorder())
			if err != nil {
				t.Fatal(err)
			}
			principal := map[string]interface{}{"uid": "account-24", "role": "member"}
			if err := current.SetAuthenticated("identity", principal); err != nil {
				t.Fatal(err)
			}
			principal["role"] = "admin"
			if current.id == oldID || len(writer.Header().Values("Set-Cookie")) != 0 {
				t.Fatal("authentication failed to rotate before cookie publication")
			}
			if got, found := current.Get("cart"); !found || got != "retained" {
				t.Fatal("rotation lost unrelated data")
			}
			if err := stale.SetAuthenticated("identity", "another-user"); !errors.Is(err, ErrSessionRevoked) || stale.Has("identity") {
				t.Fatalf("second request authenticated using revoked ID: %v", err)
			}
			if err := stale.Set("late", true); err != nil {
				t.Fatal(err)
			}
			if err := stale.Save(); !errors.Is(err, ErrSessionRevoked) {
				t.Fatalf("stale request recreated old ID: %v", err)
			}
			if err := current.Save(); err != nil {
				t.Fatal(err)
			}
			newCookies := writer.Result().Cookies()
			if len(newCookies) != 1 || newCookies[0].Value == oldCookie.Value {
				t.Fatal("new authenticated cookie missing")
			}
			for _, c := range []*http.Cookie{oldCookie, newCookies[0]} {
				resumed, err := manager.NewRequestSession(requestFromSessionCookie(c), httptest.NewRecorder())
				if err != nil {
					t.Fatal(err)
				}
				value, found := resumed.Get("identity")
				if c == oldCookie && found {
					t.Fatal("old ID recovered authenticated value")
				}
				if c != oldCookie && (!found || value.(map[string]interface{})["role"] != "member") {
					t.Fatal("new ID lost isolated authenticated value")
				}
			}
		})
	}
}

func TestSetAuthenticatedNeverAdoptsUnknownExternalID(t *testing.T) {
	const supplied = "0123456789ABCDEF0123456789ABCDEF"
	for _, kind := range []string{"memory", "file"} {
		backend := authenticationBackend(t, kind)
		manager := newTestSessionManager(t, backend, map[string]interface{}{"reject_unknown_id": true}, nil)
		t.Cleanup(func() { _ = manager.Close() })
		raw := httptest.NewRequest(http.MethodPost, "https://example.test/login", nil)
		raw.AddCookie(&http.Cookie{Name: manager.GetConfig().Name, Value: supplied})
		writer := httptest.NewRecorder()
		current, err := manager.NewRequestSession(raw, writer)
		if err != nil {
			t.Fatal(err)
		}
		if current.id == supplied {
			t.Fatal("strict mode adopted supplied ID")
		}
		if err := current.SetAuthenticated("uid", "verified-user"); err != nil {
			t.Fatal(err)
		}
		if err := current.Save(); err != nil {
			t.Fatal(err)
		}
		if _, found, err := backend.Read(supplied); err != nil || found {
			t.Fatalf("external ID stored: %t %v", found, err)
		}
	}
}

func TestSetAuthenticatedFailureDoesNotPublishIdentity(t *testing.T) {
	for _, failure := range []string{"rotation", "invalid-key", "invalid-value", "oversize", "committed"} {
		t.Run(failure, func(t *testing.T) {
			backend := newCountingDriver()
			manager := newTestSessionManager(t, backend, nil, nil)
			oldCookie, oldID := anonymousAuthenticationSession(t, manager)
			writer := httptest.NewRecorder()
			current, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), writer)
			if err != nil {
				t.Fatal(err)
			}
			key, value := "uid", interface{}("verified-user")
			want := errors.New("rotation unavailable")
			switch failure {
			case "rotation":
				backend.updateErr = want
			case "invalid-key":
				key, want = "", ErrInvalidSessionKey
			case "invalid-value":
				value, want = make(chan int), ErrInvalidSessionValue
			case "oversize":
				value, want = strings.Repeat("x", manager.GetConfig().MaxDataBytes), ErrSessionDataTooLarge
			case "committed":
				if err := current.CommitResponse(); err != nil {
					t.Fatal(err)
				}
				want = ErrSessionCommitted
			}
			if err := current.SetAuthenticated(key, value); !errors.Is(err, want) {
				t.Fatalf("error=%v want=%v", err, want)
			}
			backend.updateErr = nil
			if current.id != oldID || current.Has("uid") || len(writer.Result().Cookies()) != 0 {
				t.Fatal("failed authentication changed identity or cookie")
			}
			resumed, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), httptest.NewRecorder())
			if err != nil || resumed.Has("uid") {
				t.Fatalf("failed authentication persisted identity: %v", err)
			}
		})
	}
	var missing *Session
	if err := missing.SetAuthenticated("uid", "verified-user"); !errors.Is(err, ErrInvalidSessionDependency) {
		t.Fatal(err)
	}
	manager := newTestSessionManager(t, newCountingDriver(), nil, nil)
	if err := manager.SetAuthenticated("uid", "verified-user"); !errors.Is(err, ErrInvalidSessionDependency) {
		t.Fatal(err)
	}
}

type authenticationJSONValue struct{ calls *int }

func (value authenticationJSONValue) MarshalJSON() ([]byte, error) {
	*value.calls++
	return json.Marshal(map[string]string{"uid": "verified-user"})
}

func TestSetAuthenticatedEncodesCallerValueOnce(t *testing.T) {
	manager := newTestSessionManager(t, newCountingDriver(), nil, nil)
	current, err := manager.NewRequestSession(httptest.NewRequest(http.MethodPost, "/login", nil), httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := current.SetAuthenticated("identity", authenticationJSONValue{calls: &calls}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("caller value encoded %d times", calls)
	}
}

func TestSetAuthenticatedBlocksSaveAndResponseCommitUntilPublication(t *testing.T) {
	for _, boundary := range []string{"save", "commit"} {
		t.Run(boundary, func(t *testing.T) {
			backend := &pausingSessionDriver{countingDriver: newCountingDriver(), started: make(chan struct{}), release: make(chan struct{})}
			manager := newTestSessionManager(t, backend, nil, nil)
			oldCookie, oldID := anonymousAuthenticationSession(t, manager)
			writer := httptest.NewRecorder()
			current, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), writer)
			if err != nil {
				t.Fatal(err)
			}
			backend.pause = true
			authenticated := make(chan error, 1)
			go func() { authenticated <- current.SetAuthenticated("uid", "verified-user") }()
			<-backend.started
			boundaryStarted := make(chan struct{})
			finished := make(chan error, 1)
			go func() {
				close(boundaryStarted)
				if boundary == "commit" {
					finished <- current.CommitResponse()
				} else {
					finished <- current.Save()
				}
			}()
			<-boundaryStarted
			close(backend.release)
			if err := <-authenticated; err != nil {
				t.Fatal(err)
			}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			if current.id == oldID {
				t.Fatal("boundary saved old ID")
			}
			stored, found := backend.snapshot(current.id)
			envelope, err := decodeSessionEnvelope(stored, manager.GetConfig().MaxDataBytes)
			if err != nil || !found || string(envelope.Data["uid"]) != `"verified-user"` {
				t.Fatalf("boundary persisted incomplete authentication: found=%t err=%v", found, err)
			}
			cookies := writer.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Value != current.id {
				t.Fatal("boundary cookie did not identify new saved session")
			}
		})
	}
}

func TestSetAuthenticatedPersistenceFailureDoesNotPublishCookie(t *testing.T) {
	backend := newCountingDriver()
	manager := newTestSessionManager(t, backend, nil, nil)
	oldCookie, _ := anonymousAuthenticationSession(t, manager)
	writer := httptest.NewRecorder()
	current, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.SetAuthenticated("uid", "verified-user"); err != nil {
		t.Fatal(err)
	}
	backend.updateErr = errors.New("persistence unavailable")
	if err := current.Save(); !errors.Is(err, backend.updateErr) {
		t.Fatalf("save error lost: %v", err)
	}
	backend.updateErr = nil
	if len(writer.Result().Cookies()) != 0 {
		t.Fatal("failed save published cookie")
	}
	if _, found := backend.snapshot(current.id); found {
		t.Fatal("failed save persisted new identity")
	}
	old, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), httptest.NewRecorder())
	if err != nil || old.Has("uid") {
		t.Fatalf("old ID retained identity after failed login: %v", err)
	}
}
