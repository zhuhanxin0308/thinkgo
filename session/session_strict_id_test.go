package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const strictTestExternalID = "0123456789ABCDEF0123456789ABCDEF"

func TestRejectUnknownIDConfiguration(t *testing.T) {
	for _, raw := range []map[string]interface{}{nil, {}, {"reject_unknown_id": false}, {"reject_unknown_id": true}} {
		config, err := ParseConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := raw["reject_unknown_id"].(bool)
		if config.RejectUnknownID != want {
			t.Fatalf("reject_unknown_id=%t want=%t", config.RejectUnknownID, want)
		}
	}
	for _, invalid := range []interface{}{nil, "true", "false", 1, 0, json.Number("1"), []bool{true}} {
		config, err := ParseConfig(map[string]interface{}{"reject_unknown_id": invalid})
		if !errors.Is(err, ErrInvalidSessionConfig) || config != (Config{}) {
			t.Fatalf("invalid option accepted: %T, config=%+v err=%v", invalid, config, err)
		}
	}
	manager := newTestSessionManager(t, newCountingDriver(), map[string]interface{}{"reject_unknown_id": true}, nil)
	copy := manager.GetConfig()
	copy.RejectUnknownID = false
	if copy.RejectUnknownID || !manager.GetConfig().RejectUnknownID || DefaultConfig().RejectUnknownID {
		t.Fatal("configuration ownership or compatibility default changed")
	}
}

func strictIDRequest(t *testing.T, manager *Session, explicit bool, id string) (*Session, *httptest.ResponseRecorder, error) {
	t.Helper()
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	recorder := httptest.NewRecorder()
	if explicit {
		s, err := manager.NewRequestSessionWithIDAndSecure(raw, recorder, id, false)
		return s, recorder, err
	}
	raw.AddCookie(&http.Cookie{Name: manager.config.Name, Value: id})
	s, err := manager.NewRequestSession(raw, recorder)
	return s, recorder, err
}

func TestRejectUnknownIDAdmissionAndPersistence(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("strict=%t/explicit=%t", strict, explicit), func(t *testing.T) {
				backend := newCountingDriver()
				manager := newTestSessionManager(t, backend, map[string]interface{}{
					"name": "SID", "prefix": "tenant:", "reject_unknown_id": strict,
				}, nil)
				s, recorder, err := strictIDRequest(t, manager, explicit, strictTestExternalID)
				if err != nil {
					t.Fatal(err)
				}
				if !isSafeSessionID(s.id) || (s.id != strictTestExternalID) != strict || s.persisted {
					t.Fatalf("wrong admission decision: strict=%t persisted=%t", strict, s.persisted)
				}
				if backend.readCount != 1 || backend.updateCount != 0 || len(recorder.Result().Cookies()) != 0 {
					t.Fatal("admission eagerly wrote storage or a cookie")
				}
				if err := s.Set("uid", "user-24"); err != nil {
					t.Fatal(err)
				}
				if err := s.Save(); err != nil {
					t.Fatal(err)
				}
				content, found := backend.snapshot("tenant:" + s.id)
				envelope, decodeErr := decodeSessionEnvelope(content, manager.config.MaxDataBytes)
				if !found || decodeErr != nil || string(envelope.Data["uid"]) != `"user-24"` {
					t.Fatalf("effective session was not persisted: found=%t err=%v", found, decodeErr)
				}
				if _, sourceFound := backend.snapshot("tenant:" + strictTestExternalID); sourceFound == strict {
					t.Fatal("external ID persistence contradicts selected policy")
				}
				// Read Headers directly: Recorder.Result caches the first snapshot.
				cookies := (&http.Response{Header: recorder.Header()}).Cookies()
				if len(cookies) != 1 || cookies[0].Value != s.id || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
					t.Fatal("effective ID was not published with existing cookie protection")
				}
			})
		}
	}
}

func TestRejectUnknownIDPreservesLazyAndStoredSessions(t *testing.T) {
	for _, sample := range []struct {
		name, stored string
		wantSame     bool
		wantErr      error
	}{
		{"missing", "", false, nil},
		{"active", `{"version":1,"data":{"uid":"known"}}`, true, nil},
		{"expired", `{"version":1,"expire_at":1,"data":{"uid":"old"}}`, false, nil},
		{"revoked", `{"version":1,"expire_at":200,"revoked":true,"data":null}`, false, nil},
		{"corrupt", `{"version":99,"data":{}}`, false, ErrCorruptSession},
		{"stored-empty", "", false, ErrCorruptSession},
	} {
		t.Run(sample.name, func(t *testing.T) {
			backend := newCountingDriver()
			if sample.name != "missing" {
				backend.data[strictTestExternalID] = sample.stored
			}
			manager := newTestSessionManager(t, backend, map[string]interface{}{"name": "SID", "reject_unknown_id": true}, nil)
			manager.now = func() time.Time { return time.Unix(100, 0) }
			s, recorder, err := strictIDRequest(t, manager, false, strictTestExternalID)
			if !errors.Is(err, sample.wantErr) {
				t.Fatalf("admission error=%v want=%v", err, sample.wantErr)
			}
			if err != nil {
				if s != nil || len(recorder.Header()) != 0 {
					t.Fatal("corrupt storage became an anonymous successful session")
				}
				return
			}
			if (s.id == strictTestExternalID) != sample.wantSame || s.Has("uid") != sample.wantSame {
				t.Fatal("stored identity/expiry/revocation contract changed")
			}
			if err := s.Save(); err != nil {
				t.Fatal(err)
			}
			if backend.updateCount != 0 || backend.writeCount != 0 {
				t.Fatal("untouched session allocated a storage record")
			}
			cookies := recorder.Result().Cookies()
			if sample.wantSame {
				if len(cookies) != 0 {
					t.Fatal("untouched known session was replaced")
				}
			} else if len(cookies) != 1 || cookies[0].MaxAge != -1 {
				t.Fatal("untouched rejected ID did not clear the client cookie")
			}
			if original, _ := backend.snapshot(strictTestExternalID); original != sample.stored {
				t.Fatal("read-only admission mutated the original record")
			}
		})
	}
}

type strictIDContextReader struct{ *countingDriver }

func (d *strictIDContextReader) ReadContext(ctx context.Context, id string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	return d.Read(id)
}

func TestRejectUnknownIDReadErrorsFailClosed(t *testing.T) {
	failure := errors.New("backend unavailable")
	for _, canceled := range []bool{false, true} {
		backend := &strictIDContextReader{newCountingDriver()}
		backend.readErr = failure
		manager := newTestSessionManager(t, backend, map[string]interface{}{"name": "SID", "reject_unknown_id": true}, nil)
		raw := httptest.NewRequest(http.MethodGet, "/", nil)
		raw.AddCookie(&http.Cookie{Name: "SID", Value: strictTestExternalID})
		ctx, cancel := context.WithCancel(raw.Context())
		want := failure
		if canceled {
			cancel()
			want = context.Canceled
		}
		recorder := httptest.NewRecorder()
		s, err := manager.NewRequestSession(raw.WithContext(ctx), recorder)
		cancel()
		if s != nil || !errors.Is(err, want) || backend.updateCount != 0 || len(recorder.Header()) != 0 {
			t.Fatalf("read failure did not fail closed: session=%v err=%v", s != nil, err)
		}
	}
}

func TestRejectUnknownIDConcurrentMissesStayIsolated(t *testing.T) {
	backend := newCountingDriver()
	manager := newTestSessionManager(t, backend, map[string]interface{}{"name": "SID", "reject_unknown_id": true}, nil)
	const count = 16
	start := make(chan struct{})
	type outcome struct {
		id, value string
		err       error
	}
	results := make(chan outcome, count)
	var wg sync.WaitGroup
	for index := range count {
		wg.Go(func() {
			<-start
			value := fmt.Sprintf("user-%d", index)
			s, _, err := strictIDRequest(t, manager, true, strictTestExternalID)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			if err = s.Set("uid", value); err == nil {
				err = s.Save()
			}
			results <- outcome{id: s.id, value: value, err: err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	seen := make(map[string]bool)
	for result := range results {
		if result.err != nil || result.id == strictTestExternalID || seen[result.id] {
			t.Fatalf("concurrent identities collided or failed: %v", result.err)
		}
		seen[result.id] = true
		content, found := backend.snapshot(result.id)
		envelope, err := decodeSessionEnvelope(content, manager.config.MaxDataBytes)
		var value string
		decodeErr := json.Unmarshal(envelope.Data["uid"], &value)
		if !found || err != nil || decodeErr != nil || value != result.value {
			t.Fatal("concurrent anonymous requests shared business data")
		}
	}
	if _, found := backend.snapshot(strictTestExternalID); found || len(seen) != count {
		t.Fatal("external ID was established during concurrent misses")
	}
}

func TestRejectUnknownIDMixedManagersAndConcurrentRecordCreation(t *testing.T) {
	backend := newCountingDriver()
	legacy := newTestSessionManager(t, backend, map[string]interface{}{"name": "SID"}, nil)
	strict := newTestSessionManager(t, backend, map[string]interface{}{"name": "SID", "reject_unknown_id": true}, nil)
	pending, _, err := strictIDRequest(t, strict, true, strictTestExternalID)
	if err != nil {
		t.Fatal(err)
	}
	// A permissive node may create the external ID after the strict node's
	// read. Strict admission must not overwrite that independently saved row.
	old, _, err := strictIDRequest(t, legacy, true, strictTestExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Set("origin", "permissive-node"); err != nil {
		t.Fatal(err)
	}
	if err := old.Save(); err != nil {
		t.Fatal(err)
	}
	before, found := backend.snapshot(strictTestExternalID)
	if !found {
		t.Fatal("permissive manager no longer preserves its existing contract")
	}
	if err := pending.Set("origin", "strict-node"); err != nil {
		t.Fatal(err)
	}
	if err := pending.Save(); err != nil {
		t.Fatal(err)
	}
	if after, _ := backend.snapshot(strictTestExternalID); after != before || pending.id == strictTestExternalID {
		t.Fatal("strict miss reused or changed the subsequently created record")
	}
	// The option is not an origin signature: a later strict request accepts
	// a valid existing record even if a permissive node established its ID.
	resumed, _, err := strictIDRequest(t, strict, true, strictTestExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if value, exists := resumed.Get("origin"); !exists || value != "permissive-node" || resumed.id != strictTestExternalID {
		t.Fatal("mixed managers must preserve existing valid records")
	}
}
