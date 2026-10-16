package route

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
)

// These regressions execute the selected middleware and handler: matching the
// wrong route must never return a successful public response for a protected URL.
func TestStaticSpecificityPreservesAuthentication(t *testing.T) {
	for _, test := range []struct {
		name, broad, protected, path, broadDomain, protectedDomain, host string
	}{
		{name: "longest public prefix", broad: "/public", protected: "/public/admin", path: "/public/admin/panel"},
		{name: "deepest public prefix", broad: "/public/admin", protected: "/public/admin/panel", path: "/public/admin/panel/settings"},
		{name: "longest domain prefix", broad: "/public", protected: "/public/admin", path: "/public/admin/panel", broadDomain: "cdn.example.com", protectedDomain: "cdn.example.com"},
		{name: "public exact beats domain prefix", broad: "/api", protected: "/api/users", path: "/api/users", broadDomain: "cdn.example.com"},
		{name: "public longer prefix beats domain prefix", broad: "/api", protected: "/api/users", path: "/api/users/detail", broadDomain: "cdn.example.com"},
		{name: "domain exact beats public prefix", broad: "/api", protected: "/api/users", path: "/api/users", protectedDomain: "cdn.example.com"},
		{name: "domain wins equal prefix", broad: "/api/users", protected: "/api/users", path: "/api/users/detail", protectedDomain: "cdn.example.com"},
		{name: "domain wins equal exact", broad: "/api/users", protected: "/api/users", path: "/api/users", protectedDomain: "cdn.example.com"},
		{name: "unrelated domain excluded", broad: "/api", protected: "/api/users", path: "/api/users/detail", broadDomain: "other.example.com"},
		{name: "case insensitive longest prefix", broad: "/PUBLIC", protected: "/PUBLIC/ADMIN", path: "/public/admin/panel"},
		{name: "optional suffix full consumption", broad: "/api", protected: "/api/users", path: "/api/users/.html", broadDomain: "cdn.example.com"},
		{name: "Unicode domain exact fallback", broad: "/api/status", protected: "/api/ſtatus", path: "/api/status", protectedDomain: "cdn.example.com"},
	} {
		for _, protectedFirst := range []bool{false, true} {
			for _, authorized := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/protected-first=%t/authorized=%t", test.name, protectedFirst, authorized), func(t *testing.T) {
					router := NewRouter() // Retain the default non-complete matching policy.
					var authCalls, publicCalls, protectedCalls int
					auth := func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
						authCalls++
						if req.Raw().Header.Get("Authorization") != "Bearer allowed" {
							return fwcontext.NewResponse().Code(http.StatusUnauthorized)
						}
						return next(req)
					}
					register := func(protected bool) {
						t.Helper()
						path, domain := test.broad, test.broadDomain
						var handlers []middleware.Handler
						if protected {
							path, domain = test.protected, test.protectedDomain
							handlers = []middleware.Handler{auth}
						}
						handler := func(*fwcontext.Request) *fwcontext.Response {
							if protected {
								protectedCalls++
								return fwcontext.NewResponse().Content("protected")
							}
							publicCalls++
							return fwcontext.NewResponse().Content("public")
						}
						var err error
						if domain != "" {
							err = router.Domain(domain, func(group *Group) error {
								_, err := group.Get(path, handler, handlers...)
								return err
							})
						} else {
							_, err = router.Get(path, handler, handlers...)
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					register(protectedFirst)
					register(!protectedFirst)
					host := test.host
					if host == "" {
						host = "cdn.example.com"
					}
					raw := httptest.NewRequest(http.MethodGet, "http://"+host+test.path, nil)
					if authorized {
						raw.Header.Set("Authorization", "Bearer allowed")
					}
					request := fwcontext.MustNewRequest(raw)
					matched, _, err := router.Match(request)
					if err != nil || matched == nil {
						t.Fatalf("protected URL did not match: route=%v err=%v", matched, err)
					}
					response := matched.ExecuteMiddleware(request, matched.Handler().(func(*fwcontext.Request) *fwcontext.Response))
					wantStatus, wantCalls := http.StatusUnauthorized, 0
					if authorized {
						wantStatus, wantCalls = http.StatusOK, 1
					}
					if response.GetStatus() != wantStatus || authCalls != 1 || publicCalls != 0 || protectedCalls != wantCalls {
						t.Fatalf("authentication boundary bypassed: status=%d auth=%d public=%d protected=%d", response.GetStatus(), authCalls, publicCalls, protectedCalls)
					}
					if authorized && string(response.GetBody()) != "protected" {
						t.Fatalf("wrong handler executed: %q", response.GetBody())
					}
				})
			}
		}
	}
}

func TestStaticSpecificityKeepsCompleteMatchAndCandidateValidation(t *testing.T) {
	for _, complete := range []bool{false, true} {
		router := NewRouter()
		if err := router.SetCompleteMatch(complete); err != nil {
			t.Fatal(err)
		}
		if _, err := router.Get("/api", "Broad@Public"); err != nil {
			t.Fatal(err)
		}
		protected, err := router.Get("/api/users", "Users@Protected")
		if err != nil {
			t.Fatal(err)
		}
		if err := protected.WithExtension("json"); err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct{ path, want string }{
			{path: "/api/users.json", want: "Users@Protected"},
			{path: "/api/users/.json", want: "Users@Protected"},
			{path: "/api/users", want: "Broad@Public"},
			{path: "/api/users/detail.json", want: "Users@Protected"},
			{path: "/api/users/detail.JSON", want: "Broad@Public"},
			{path: "/api/users/detail.json/"},
		} {
			want := test.want
			if complete && test.path != "/api/users.json" && test.path != "/api/users/.json" {
				want = ""
			}
			request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com"+test.path, nil))
			matched, _, err := router.Match(request)
			if err != nil {
				t.Fatal(err)
			}
			if want == "" && matched != nil || want != "" && (matched == nil || matched.Handler() != want) {
				t.Fatalf("complete=%t path=%s: route=%v want=%s", complete, test.path, matched, want)
			}
		}
	}
}

func TestStaticExactSpecificityAndFoldAliasesPreserveAuthentication(t *testing.T) {
	for _, complete := range []bool{false, true} {
		for _, domain := range []string{"", "cdn.example.com"} {
			for _, aliases := range [][2]string{{"/ſtatus", "/status"}, {"/Σ", "/ς"}, {"/K", "/k"}} {
				t.Run(fmt.Sprintf("complete=%t/domain=%s/%s", complete, domain, aliases[0]), func(t *testing.T) {
					router := NewRouter()
					if err := router.SetCompleteMatch(complete); err != nil {
						t.Fatal(err)
					}
					var authCalls, publicCalls int
					register := func(group *Group) error {
						if _, err := group.Get(aliases[0], func(*fwcontext.Request) *fwcontext.Response {
							t.Fatal("unauthenticated protected handler executed")
							return nil
						}, func(*fwcontext.Request, func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
							authCalls++
							return fwcontext.NewResponse().Code(http.StatusUnauthorized)
						}); err != nil {
							return err
						}
						_, err := group.Get(aliases[1], func(*fwcontext.Request) *fwcontext.Response {
							publicCalls++
							return fwcontext.NewResponse()
						})
						return err
					}
					if err := router.Domain(domain, register); err != nil {
						t.Fatal(err)
					}
					request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://cdn.example.com"+aliases[1], nil))
					matched, _, err := router.Match(request)
					if err != nil || matched == nil {
						t.Fatalf("alias match: route=%v err=%v", matched, err)
					}
					response := matched.ExecuteMiddleware(request, matched.Handler().(func(*fwcontext.Request) *fwcontext.Response))
					if response.GetStatus() != http.StatusUnauthorized || authCalls != 1 || publicCalls != 0 {
						t.Fatalf("fold alias bypassed earlier authentication: status=%d auth=%d public=%d", response.GetStatus(), authCalls, publicCalls)
					}
				})
			}
		}
	}
}

func TestStaticExactLongerLiteralBeatsShorterExtensionDomain(t *testing.T) {
	router := NewRouter()
	var authCalls, publicCalls int
	if _, err := router.Get("/api/.json", func(*fwcontext.Request) *fwcontext.Response {
		t.Fatal("unauthenticated handler executed")
		return nil
	}, func(*fwcontext.Request, func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		authCalls++
		return fwcontext.NewResponse().Code(http.StatusUnauthorized)
	}); err != nil {
		t.Fatal(err)
	}
	if err := router.Domain("cdn.example.com", func(group *Group) error {
		registered, err := group.Get("/api", func(*fwcontext.Request) *fwcontext.Response {
			publicCalls++
			return fwcontext.NewResponse()
		})
		if err != nil {
			return err
		}
		return registered.WithExtension("json")
	}); err != nil {
		t.Fatal(err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://cdn.example.com/api/.json", nil))
	matched, _, err := router.Match(request)
	if err != nil || matched == nil {
		t.Fatalf("exact match: route=%v err=%v", matched, err)
	}
	response := matched.ExecuteMiddleware(request, matched.Handler().(func(*fwcontext.Request) *fwcontext.Response))
	if response.GetStatus() != http.StatusUnauthorized || authCalls != 1 || publicCalls != 0 {
		t.Fatalf("shorter domain bypassed exact authentication: status=%d auth=%d public=%d", response.GetStatus(), authCalls, publicCalls)
	}
}

func TestStaticIndexKeysFollowEqualFold(t *testing.T) {
	router := NewRouter()
	for _, pair := range [][2]string{{"s", "S"}, {"s", "ſ"}, {"Σ", "σ"}, {"Σ", "ς"}, {"k", "K"}, {"k", "K"}, {"İ", "i"}, {"ı", "i"}, {"ß", "ss"}} {
		left := router.pathPartsKey([]string{pair[0]})
		right := router.pathPartsKey([]string{pair[1]})
		if equal := left == right; equal != strings.EqualFold(pair[0], pair[1]) {
			t.Fatalf("index collapsed or split fold class %q/%q: %q/%q", pair[0], pair[1], left, right)
		}
	}
}
