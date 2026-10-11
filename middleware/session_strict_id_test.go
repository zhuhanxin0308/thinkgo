package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	"github.com/zhuhanxin0308/thinkgo/v3/session"
	"github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

func strictSessionBackend(t *testing.T, kind string) session.Driver {
	t.Helper()
	if kind == "file" {
		backend, err := driver.NewFile(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return backend
	}
	backend, err := driver.NewMemoryWithMaxEntries(128)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func strictSessionMiddleware(t *testing.T, backend session.Driver) *Session {
	t.Helper()
	factory, err := cookie.NewCookie(nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.NewSession(map[string]interface{}{
		"name": "SID", "reject_unknown_id": true, "var_session_id": "upload_sid",
	}, backend, factory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return &Session{Manager: manager}
}

func strictResponseCookie(t *testing.T, response *fwcontext.Response) *http.Cookie {
	t.Helper()
	if response == nil || response.GetStatus() != http.StatusOK {
		t.Fatal("middleware did not return a successful response")
	}
	cookies := (&http.Response{Header: response.Headers()}).Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge < 0 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("expected one protected session cookie")
	}
	return cookies[0]
}

func strictSessionRaw(c *http.Cookie) *http.Request {
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/account", nil)
	if c != nil {
		raw.AddCookie(c)
	}
	return raw
}

func TestSessionStrictIDMiddlewareRejectsUnknownSources(t *testing.T) {
	const supplied = "0123456789ABCDEF0123456789ABCDEF"
	for _, kind := range []string{"memory", "file"} {
		for _, source := range []string{"cookie", "post", "post_with_known_cookie"} {
			t.Run(kind+"/"+source, func(t *testing.T) {
				backend := strictSessionBackend(t, kind)
				mw := strictSessionMiddleware(t, backend)
				raw := strictSessionRaw(&http.Cookie{Name: "SID", Value: supplied})
				if source != "cookie" {
					form := url.Values{"upload_sid": {supplied}}
					raw = httptest.NewRequest(http.MethodPost, "http://example.com/upload", strings.NewReader(form.Encode()))
					raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}
				if source == "post_with_known_cookie" {
					known := mw.Handle(fwcontext.MustNewRequest(strictSessionRaw(nil)), func(req *fwcontext.Request) *fwcontext.Response {
						if err := req.GetData("_session").(*session.Session).Set("uid", "cookie-account"); err != nil {
							t.Fatal(err)
						}
						return fwcontext.NewResponse().Content("known")
					})
					raw.AddCookie(strictResponseCookie(t, known))
				}
				response := mw.Handle(fwcontext.MustNewRequest(raw), func(req *fwcontext.Request) *fwcontext.Response {
					s := req.GetData("_session").(*session.Session)
					if s.Has("uid") {
						t.Fatal("rejected POST ID fell back to a different cookie identity")
					}
					if err := s.Set("cart", "item-24"); err != nil {
						t.Fatal(err)
					}
					return fwcontext.NewResponse().Content("ok")
				})
				effective := strictResponseCookie(t, response)
				if effective.Value == supplied {
					t.Fatal("unknown external ID was accepted by the real middleware")
				}
				if _, found, err := backend.Read(supplied); err != nil || found {
					t.Fatalf("external ID was stored: found=%t err=%v", found, err)
				}
				followup, err := mw.Manager.NewRequestSession(strictSessionRaw(effective), httptest.NewRecorder())
				if err != nil {
					t.Fatal(err)
				}
				if value, found := followup.Get("cart"); !found || value != "item-24" {
					t.Fatal("new session cookie did not resume its saved data")
				}
			})
		}
	}
}

func TestSessionStrictIDStillRequiresAuthenticationRotation(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			backend := strictSessionBackend(t, kind)
			mw := strictSessionMiddleware(t, backend)
			anonymous := mw.Handle(fwcontext.MustNewRequest(strictSessionRaw(nil)), func(req *fwcontext.Request) *fwcontext.Response {
				if err := req.GetData("_session").(*session.Session).Set("cart", "retained"); err != nil {
					t.Fatal(err)
				}
				return fwcontext.NewResponse().Content("anonymous")
			})
			oldCookie := strictResponseCookie(t, anonymous)
			stale, err := mw.Manager.NewRequestSession(strictSessionRaw(oldCookie), httptest.NewRecorder())
			if err != nil {
				t.Fatal(err)
			}
			// Credential verification is an application decision, already successful
			// in this scenario. Rotate before adding the authenticated principal.
			login := mw.Handle(fwcontext.MustNewRequest(strictSessionRaw(oldCookie)), func(req *fwcontext.Request) *fwcontext.Response {
				s := req.GetData("_session").(*session.Session)
				if value, found := s.Get("cart"); !found || value != "retained" {
					t.Fatal("known anonymous session was not restored")
				}
				if err := s.Regenerate(); err != nil {
					return fwcontext.NewResponse().Code(http.StatusInternalServerError)
				}
				if err := s.Set("uid", "account-24"); err != nil {
					t.Fatal(err)
				}
				return fwcontext.NewResponse().Content("authenticated")
			})
			newCookie := strictResponseCookie(t, login)
			if newCookie.Value == oldCookie.Value {
				t.Fatal("authentication retained the pre-authentication ID")
			}
			if err := stale.Set("uid", "stale-request"); err != nil {
				t.Fatal(err)
			}
			if err := stale.Save(); !errors.Is(err, session.ErrSessionRevoked) {
				t.Fatalf("stale request resurrected the old ID: %v", err)
			}
			for _, check := range []struct {
				cookie *http.Cookie
				known  bool
			}{{oldCookie, false}, {newCookie, true}} {
				s, err := mw.Manager.NewRequestSession(strictSessionRaw(check.cookie), httptest.NewRecorder())
				if err != nil {
					t.Fatal(err)
				}
				uid, found := s.Get("uid")
				if found != check.known || (check.known && uid != "account-24") {
					t.Fatal("authentication or old-ID revocation did not persist")
				}
			}
		})
	}
}

type strictSessionUpdateFailure struct {
	session.Driver
	cause error
}

func (d *strictSessionUpdateFailure) Update(string, func(string, bool) (string, bool, error)) error {
	return d.cause
}

func TestSessionStrictIDRotationFailureDoesNotPublishLogin(t *testing.T) {
	backend := strictSessionBackend(t, "memory")
	mw := strictSessionMiddleware(t, backend)
	anonymous := mw.Handle(fwcontext.MustNewRequest(strictSessionRaw(nil)), func(req *fwcontext.Request) *fwcontext.Response {
		if err := req.GetData("_session").(*session.Session).Set("cart", "anonymous"); err != nil {
			t.Fatal(err)
		}
		return fwcontext.NewResponse().Content("anonymous")
	})
	c := strictResponseCookie(t, anonymous)
	failure := errors.New("revocation unavailable")
	failing := strictSessionMiddleware(t, &strictSessionUpdateFailure{Driver: backend, cause: failure})
	response := failing.Handle(fwcontext.MustNewRequest(strictSessionRaw(c)), func(req *fwcontext.Request) *fwcontext.Response {
		s := req.GetData("_session").(*session.Session)
		if err := s.Regenerate(); !errors.Is(err, failure) {
			t.Fatalf("unexpected rotation outcome: %v", err)
		}
		return fwcontext.NewResponse().Code(http.StatusServiceUnavailable).Content("login unavailable")
	})
	if response.GetStatus() != http.StatusServiceUnavailable || len(response.Headers().Values("Set-Cookie")) != 0 {
		t.Fatal("failed rotation became a login success or new cookie")
	}
	original, err := mw.Manager.NewRequestSession(strictSessionRaw(c), httptest.NewRecorder())
	if err != nil || original.Has("uid") {
		t.Fatalf("failed rotation changed authentication state: %v", err)
	}
}
