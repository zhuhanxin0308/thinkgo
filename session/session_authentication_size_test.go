package session

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func seedAuthenticationSizeSession(t *testing.T, manager *Session, padding int) (*http.Cookie, string) {
	t.Helper()
	writer := httptest.NewRecorder()
	current, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/login", nil), writer)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"captcha": "captcha-before-login", "csrf": "csrf-before-login", "padding": strings.Repeat("p", padding)} {
		if err := current.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := current.Save(); err != nil {
		t.Fatal(err)
	}
	return writer.Result().Cookies()[0], current.id
}

func TestSetAuthenticatedOversizePreservesOldSessionAcrossBackends(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		for _, latestBackend := range []bool{false, true} {
			name := kind + "/local"
			if latestBackend {
				name = kind + "/latest-backend"
			}
			t.Run(name, func(t *testing.T) {
				backend := authenticationBackend(t, kind)
				manager := newTestSessionManager(t, backend, map[string]interface{}{"max_data_bytes": 512, "expire": 0, "reject_unknown_id": true}, nil)
				t.Cleanup(func() { _ = manager.Close() })
				padding := 300
				if latestBackend {
					padding = 40
				}
				oldCookie, oldID := seedAuthenticationSizeSession(t, manager, padding)
				writer := httptest.NewRecorder()
				current, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), writer)
				if err != nil {
					t.Fatal(err)
				}
				if err := current.Set("local", "unsaved"); err != nil {
					t.Fatal(err)
				}
				if latestBackend {
					other, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), httptest.NewRecorder())
					if err != nil {
						t.Fatal(err)
					}
					if err := other.Set("padding", strings.Repeat("p", 300)); err != nil {
						t.Fatal(err)
					}
					if err := other.Save(); err != nil {
						t.Fatal(err)
					}
				}
				storedBefore, found, err := backend.Read(oldID)
				if err != nil || !found {
					t.Fatalf("missing baseline: %v", err)
				}
				beforeState, beforeData := current.snapshotForSave(), current.All()
				identity := strings.Repeat("u", 200)
				if err := current.SetAuthenticated("identity", identity); !errors.Is(err, ErrSessionDataTooLarge) {
					t.Fatalf("merged overflow: %v", err)
				}
				storedAfter, found, err := backend.Read(oldID)
				if err != nil || !found || storedAfter != storedBefore {
					t.Fatalf("size rejection changed persisted old session: found=%t err=%v", found, err)
				}
				if !reflect.DeepEqual(beforeState, current.snapshotForSave()) || !reflect.DeepEqual(beforeData, current.All()) || current.id != oldID || len(writer.Header().Values("Set-Cookie")) != 0 {
					t.Fatal("size rejection changed local identity, mutations, data or cookie")
				}
				resumed, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), httptest.NewRecorder())
				if err != nil {
					t.Fatal(err)
				}
				if resumed.id != oldID || resumed.Has("identity") {
					t.Fatal("old cookie was revoked by failed authentication")
				}
				for key, want := range map[string]string{"captcha": "captcha-before-login", "csrf": "csrf-before-login"} {
					if got, ok := resumed.Get(key); !ok || got != want {
						t.Fatalf("old cookie lost %s: %v", key, got)
					}
				}
				// The same request can still save pending pre-login data under the old ID.
				if err := current.Save(); err != nil || current.id != oldID {
					t.Fatalf("failed login made old session unusable: %v", err)
				}
				if err := current.Delete("padding"); err != nil {
					t.Fatal(err)
				}
				if err := current.SetAuthenticated("identity", identity); err != nil {
					t.Fatal(err)
				}
				if current.id == oldID {
					t.Fatal("successful retry did not rotate")
				}
				revoked, found, err := backend.Read(oldID)
				if err != nil || !found {
					t.Fatal(err)
				}
				envelope, err := decodeSessionEnvelope(revoked, 512)
				if err != nil || !envelope.Revoked || len(envelope.Data) != 0 {
					t.Fatalf("successful retry did not revoke old ID: %v", err)
				}
				if err := resumed.Set("stale", true); err != nil {
					t.Fatal(err)
				}
				if err := resumed.Save(); !errors.Is(err, ErrSessionRevoked) {
					t.Fatalf("stale request restored old ID: %v", err)
				}
				if err := current.Save(); err != nil {
					t.Fatal(err)
				}
				cookies := writer.Result().Cookies()
				if len(cookies) == 0 {
					t.Fatal("successful retry did not publish a cookie")
				}
				authenticated, err := manager.NewRequestSession(requestFromSessionCookie(cookies[len(cookies)-1]), httptest.NewRecorder())
				if err != nil {
					t.Fatal(err)
				}
				if got, ok := authenticated.Get("identity"); !ok || got != identity {
					t.Fatal("retry did not persist authenticated identity")
				}
			})
		}
	}
}

func TestSetAuthenticatedSizePreflightHandlesReplacementClearAndExactLimit(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		for _, operation := range []string{"replace", "clear", "exact-limit"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				backend := authenticationBackend(t, kind)
				manager := newTestSessionManager(t, backend, map[string]interface{}{"max_data_bytes": 512, "expire": 0}, nil)
				t.Cleanup(func() { _ = manager.Close() })
				oldCookie, oldID := seedAuthenticationSizeSession(t, manager, 100)
				current, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), httptest.NewRecorder())
				if err != nil {
					t.Fatal(err)
				}
				identity := "verified-user"
				switch operation {
				case "replace":
					if err := current.Set("identity", strings.Repeat("x", 200)); err != nil {
						t.Fatal(err)
					}
				case "clear":
					if err := current.Clear(); err != nil {
						t.Fatal(err)
					}
				case "exact-limit":
					if err := current.Clear(); err != nil {
						t.Fatal(err)
					}
					empty, err := json.Marshal(sessionEnvelope{Version: 1, Data: map[string]json.RawMessage{"identity": json.RawMessage(`""`)}})
					if err != nil {
						t.Fatal(err)
					}
					identity = strings.Repeat("u", 512-len(empty))
				}
				if err := current.SetAuthenticated("identity", identity); err != nil {
					t.Fatal(err)
				}
				if current.id == oldID {
					t.Fatal("authentication did not rotate")
				}
				if err := current.Save(); err != nil {
					t.Fatal(err)
				}
				stored, found, err := backend.Read(current.id)
				if err != nil || !found {
					t.Fatalf("new identity not stored: %v", err)
				}
				envelope, err := decodeSessionEnvelope(stored, 512)
				if err != nil {
					t.Fatal(err)
				}
				if operation != "replace" && len(envelope.Data) != 1 {
					t.Fatal("clear did not remove pre-login data")
				}
				if operation == "exact-limit" && len(stored) != 512 {
					t.Fatalf("exact limit not exercised: %d", len(stored))
				}
			})
		}
	}
}

func TestSetAuthenticatedOversizePreservesPrivilegeAndUnpersistedState(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		for _, persisted := range []bool{false, true} {
			name := kind + "/new"
			if persisted {
				name = kind + "/privilege"
			}
			t.Run(name, func(t *testing.T) {
				backend := authenticationBackend(t, kind)
				manager := newTestSessionManager(t, backend, map[string]interface{}{"max_data_bytes": 512, "expire": 600}, nil)
				t.Cleanup(func() { _ = manager.Close() })
				writer := httptest.NewRecorder()
				current, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/login", nil), writer)
				if err != nil {
					t.Fatal(err)
				}
				for key, value := range map[string]string{"role": "member", "captcha": "kept", "padding": strings.Repeat("p", 300)} {
					if err := current.Set(key, value); err != nil {
						t.Fatal(err)
					}
				}
				var oldCookie *http.Cookie
				if persisted {
					if err := current.Save(); err != nil {
						t.Fatal(err)
					}
					oldCookie = writer.Result().Cookies()[0]
					writer = httptest.NewRecorder()
					current, err = manager.NewRequestSession(requestFromSessionCookie(oldCookie), writer)
					if err != nil {
						t.Fatal(err)
					}
				}
				oldID, beforeState, beforeData := current.id, current.snapshotForSave(), current.All()
				storedBefore, foundBefore, err := backend.Read(oldID)
				if err != nil {
					t.Fatal(err)
				}
				if err := current.SetAuthenticated("role", strings.Repeat("a", 200)); !errors.Is(err, ErrSessionDataTooLarge) {
					t.Fatal(err)
				}
				storedAfter, foundAfter, err := backend.Read(oldID)
				if err != nil || foundAfter != foundBefore || storedBefore != storedAfter || !reflect.DeepEqual(beforeState, current.snapshotForSave()) || !reflect.DeepEqual(beforeData, current.All()) || len(writer.Header().Values("Set-Cookie")) != 0 {
					t.Fatalf("rejected privilege changed session: %v", err)
				}
				if persisted {
					resumed, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), httptest.NewRecorder())
					if err != nil {
						t.Fatal(err)
					}
					if value, ok := resumed.Get("role"); !ok || value != "member" || resumed.id != oldID {
						t.Fatal("rejected privilege lost original permission")
					}
				}
			})
		}
	}
}

type authenticationUpdateGate struct {
	Driver
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (gate *authenticationUpdateGate) Update(id string, update func(string, bool) (string, bool, error)) error {
	if gate.armed.Load() {
		gate.once.Do(func() { close(gate.entered) })
		<-gate.release
	}
	return gate.Driver.Update(id, update)
}

func TestSetAuthenticatedSerializesConcurrentSizeBudgetAcrossBackends(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			gate := &authenticationUpdateGate{Driver: authenticationBackend(t, kind), entered: make(chan struct{}), release: make(chan struct{})}
			manager := newTestSessionManager(t, gate, map[string]interface{}{"max_data_bytes": 512, "expire": 0}, nil)
			t.Cleanup(func() { _ = manager.Close() })
			oldCookie, oldID := seedAuthenticationSizeSession(t, manager, 250)
			writer := httptest.NewRecorder()
			current, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), writer)
			if err != nil {
				t.Fatal(err)
			}
			gate.armed.Store(true)
			authenticated, modified := make(chan error, 1), make(chan error, 1)
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate.release) }) }
			go func() { authenticated <- current.SetAuthenticated("identity", strings.Repeat("u", 100)) }()
			defer release()
			<-gate.entered
			started := make(chan struct{})
			go func() { close(started); modified <- current.Set("concurrent", strings.Repeat("c", 100)) }()
			// Release and join both operations even if later assertions fail.
			<-started
			release()
			authErr, mutationErr := <-authenticated, <-modified
			if authErr != nil {
				t.Fatalf("authentication lost its prepared byte budget: %v", authErr)
			}
			if !errors.Is(mutationErr, ErrSessionDataTooLarge) || current.Has("concurrent") {
				t.Fatalf("concurrent mutation exceeded authenticated budget: %v", mutationErr)
			}
			if current.id == oldID {
				t.Fatal("authentication did not rotate")
			}
			if err := current.Save(); err != nil {
				t.Fatal(err)
			}
			stored, found, err := gate.Driver.Read(current.id)
			if err != nil || !found || len(stored) > 512 {
				t.Fatalf("authenticated state not saved within budget: %v", err)
			}
			resumed, err := manager.NewRequestSession(requestFromSessionCookie(writer.Result().Cookies()[0]), httptest.NewRecorder())
			if err != nil {
				t.Fatal(err)
			}
			if value, ok := resumed.Get("identity"); !ok || value != strings.Repeat("u", 100) {
				t.Fatal("concurrent mutation lost authenticated value")
			}
		})
	}
}

func TestSetAuthenticatedPreflightsActualEnvelopeEncoding(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			backend := authenticationBackend(t, kind)
			manager := newTestSessionManager(t, backend, map[string]interface{}{"max_data_bytes": 512, "expire": 0}, nil)
			t.Cleanup(func() { _ = manager.Close() })
			oldCookie, oldID := seedAuthenticationSizeSession(t, manager, 0)
			// Valid driver data need not use encoding/json's HTML-escaped form.
			original := `{"version":1,"data":{"captcha":"kept","padding":"` + strings.Repeat("<", 80) + `"}}`
			if err := backend.Write(oldID, original); err != nil {
				t.Fatal(err)
			}
			writer := httptest.NewRecorder()
			current, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), writer)
			if err != nil {
				t.Fatal(err)
			}
			beforeState, beforeData := current.snapshotForSave(), current.All()
			if err := current.SetAuthenticated("identity", "verified-user"); !errors.Is(err, ErrSessionDataTooLarge) {
				t.Fatalf("actual escaped envelope was not preflighted: %v", err)
			}
			stored, found, err := backend.Read(oldID)
			if err != nil || !found || stored != original || !reflect.DeepEqual(beforeState, current.snapshotForSave()) || !reflect.DeepEqual(beforeData, current.All()) || len(writer.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("encoding overflow revoked or changed old session: %v", err)
			}
			resumed, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), httptest.NewRecorder())
			if err != nil {
				t.Fatal(err)
			}
			if resumed.id != oldID || resumed.Has("identity") {
				t.Fatal("encoding overflow lost old-cookie recovery")
			}
			if value, ok := resumed.Get("captcha"); !ok || value != "kept" {
				t.Fatal("encoding overflow lost captcha")
			}
		})
	}
}

func TestSetAuthenticatedChecksExpiryAfterDriverWait(t *testing.T) {
	gate := &authenticationUpdateGate{Driver: authenticationBackend(t, "memory"), entered: make(chan struct{}), release: make(chan struct{})}
	manager := newTestSessionManager(t, gate, map[string]interface{}{"expire": 1}, nil)
	var clock atomic.Int64
	clock.Store(time.Now().Unix())
	manager.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	oldCookie, oldID := seedAuthenticationSizeSession(t, manager, 0)
	current, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	before, found, err := gate.Driver.Read(oldID)
	if err != nil || !found {
		t.Fatal(err)
	}
	gate.armed.Store(true)
	completed := make(chan error, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	defer release()
	go func() { completed <- current.SetAuthenticated("identity", "verified-user") }()
	<-gate.entered
	clock.Add(2)
	release()
	if err := <-completed; !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("expired record authenticated after waiting for driver: %v", err)
	}
	after, found, err := gate.Driver.Read(oldID)
	if err != nil || !found || before != after || current.id != oldID || current.Has("identity") {
		t.Fatalf("expired attempt changed old record: %v", err)
	}
}

type authenticationRejectCommitDriver struct {
	Driver
	reject  atomic.Bool
	failure error
}

func (backend *authenticationRejectCommitDriver) Update(id string, update func(string, bool) (string, bool, error)) error {
	return backend.Driver.Update(id, func(data string, found bool) (string, bool, error) {
		next, remove, err := update(data, found)
		if err == nil && backend.reject.Load() {
			return "", false, backend.failure
		}
		return next, remove, err
	})
}

func TestSetAuthenticatedRejectedCommitKeepsPreparedIdentityPrivate(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			failure := errors.New("candidate rejected before commit")
			backend := &authenticationRejectCommitDriver{Driver: authenticationBackend(t, kind), failure: failure}
			manager := newTestSessionManager(t, backend, nil, nil)
			t.Cleanup(func() { _ = manager.Close() })
			oldCookie, oldID := seedAuthenticationSizeSession(t, manager, 0)
			writer := httptest.NewRecorder()
			current, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), writer)
			if err != nil {
				t.Fatal(err)
			}
			beforeState, beforeData := current.snapshotForSave(), current.All()
			before, found, err := backend.Read(oldID)
			if err != nil || !found {
				t.Fatal(err)
			}
			backend.reject.Store(true)
			if err := current.SetAuthenticated("identity", "verified-user"); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			after, found, err := backend.Read(oldID)
			if err != nil || !found || before != after || !reflect.DeepEqual(beforeState, current.snapshotForSave()) || !reflect.DeepEqual(beforeData, current.All()) || len(writer.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("rejected storage commit published candidate state: %v", err)
			}
			backend.reject.Store(false)
			if err := current.SetAuthenticated("identity", "verified-user"); err != nil {
				t.Fatal(err)
			}
			if current.id == oldID {
				t.Fatal("retry did not rotate")
			}
			if err := current.Save(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSetAuthenticatedCanonicalizesBudgetBeforeConcurrentSet(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			gate := &authenticationUpdateGate{Driver: authenticationBackend(t, kind), entered: make(chan struct{}), release: make(chan struct{})}
			manager := newTestSessionManager(t, gate, map[string]interface{}{"max_data_bytes": 512, "expire": 0}, nil)
			t.Cleanup(func() { _ = manager.Close() })
			oldCookie, oldID := seedAuthenticationSizeSession(t, manager, 0)
			original := `{"version":1,"data":{"captcha":"kept","padding":"` + strings.Repeat("<", 50) + `"}}`
			if err := gate.Driver.Write(oldID, original); err != nil {
				t.Fatal(err)
			}
			writer := httptest.NewRecorder()
			current, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), writer)
			if err != nil {
				t.Fatal(err)
			}
			gate.armed.Store(true)
			authenticated, modified := make(chan error, 1), make(chan error, 1)
			var once sync.Once
			release := func() { once.Do(func() { close(gate.release) }) }
			defer release()
			go func() { authenticated <- current.SetAuthenticated("identity", "verified-user") }()
			<-gate.entered
			go func() { modified <- current.Set("concurrent", strings.Repeat("c", 200)) }()
			release()
			authErr, setErr := <-authenticated, <-modified
			if authErr != nil {
				t.Fatal(authErr)
			}
			if !errors.Is(setErr, ErrSessionDataTooLarge) || current.Has("concurrent") {
				t.Fatalf("Set did not use canonical authenticated budget: %v", setErr)
			}
			if current.id == oldID {
				t.Fatal("successful authentication did not rotate")
			}
			if err := current.Save(); err != nil {
				t.Fatalf("preflighted identity could not save: %v", err)
			}
			stored, found, err := gate.Driver.Read(current.id)
			if err != nil || !found || len(stored) > 512 {
				t.Fatalf("canonical identity exceeds persisted budget: %v", err)
			}
			resumed, err := manager.NewRequestSession(requestFromSessionCookie(writer.Result().Cookies()[0]), httptest.NewRecorder())
			if err != nil {
				t.Fatal(err)
			}
			if value, ok := resumed.Get("padding"); !ok || value != strings.Repeat("<", 50) {
				t.Fatal("canonicalization changed the retained value")
			}
			if value, ok := resumed.Get("identity"); !ok || value != "verified-user" {
				t.Fatal("canonicalization lost authenticated identity")
			}
		})
	}
}
