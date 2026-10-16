package context_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
)

func TestRequestCookieRejectsAmbiguityAcrossAccessors(t *testing.T) {
	for _, headers := range [][]string{
		{"uid=alice; uid=attacker-controlled"},
		{"uid=alice", "uid=attacker-controlled"},
		{"uid=alice; uid=alice"},
		{`uid=""; uid=alice`},
	} {
		for _, override := range []string{"none", "unrelated", "same-name", "all", "nil"} {
			t.Run(strings.Join(headers, "/")+"/"+override, func(t *testing.T) {
				raw := httptest.NewRequest(http.MethodGet, "/", nil)
				for _, header := range headers {
					raw.Header.Add("Cookie", header)
				}
				request := fwcontext.MustNewRequest(raw)
				switch override {
				case "unrelated":
					request.SetCookie("theme", "dark")
				case "same-name":
					request.SetCookie("uid", "override")
				case "all":
					request.WithCookie(map[string]interface{}{"uid": "override"})
				case "nil":
					request.WithCookie(nil)
				}
				filterCalls := 0
				request.Filter(func(string) string {
					filterCalls++
					return "filter-must-not-authorize"
				})
				if got := request.Cookie("uid", "default-must-not-authorize"); got != "" {
					t.Fatalf("ambiguous cookie authorized value %q", got)
				}
				value, found, err := request.CookieStrict("uid")
				if value != "" || found || !errors.Is(err, cookie.ErrDuplicateCookie) {
					t.Fatalf("strict accessor: value=%q found=%v err=%v", value, found, err)
				}
				manager, err := cookie.NewCookieForRequest(cookie.DefaultConfig(), raw, nil)
				if err != nil {
					t.Fatal(err)
				}
				value, found, err = manager.Get("uid")
				if value != "" || found || !errors.Is(err, cookie.ErrDuplicateCookie) {
					t.Fatalf("cookie service disagrees: value=%q found=%v err=%v", value, found, err)
				}
				if filterCalls != 0 {
					t.Fatalf("rejected cookie ran filters %d times", filterCalls)
				}
			})
		}
	}
}

func TestRequestCookieStrictPreservesValuesAndOverrides(t *testing.T) {
	raw := httptest.NewRequest(http.MethodGet, "/", nil)
	raw.Header.Set("Cookie", `uid=alice; empty=; UID=upper; other=a; other=b`)
	request := fwcontext.MustNewRequest(raw)
	for _, test := range []struct {
		name, want string
		found      bool
	}{
		{"uid", "alice", true}, {"empty", "", true}, {"UID", "upper", true}, {"missing", "", false},
	} {
		value, found, err := request.CookieStrict(test.name)
		if value != test.want || found != test.found || err != nil {
			t.Fatalf("%s: value=%q found=%v err=%v", test.name, value, found, err)
		}
	}
	request.Filter(strings.ToUpper)
	if got := request.Cookie("missing", "fallback"); got != "FALLBACK" {
		t.Fatalf("legacy missing/default/filter changed: %q", got)
	}
	if value, found, err := request.CookieStrict("missing"); value != "" || found || err != nil {
		t.Fatalf("missing strict cookie: %q %v %v", value, found, err)
	}
	request.SetCookie("uid", "override")
	if value, found, err := request.CookieStrict("uid"); value != "OVERRIDE" || !found || err != nil {
		t.Fatalf("single override: %q %v %v", value, found, err)
	}
	request.WithCookie(map[string]interface{}{"number": 42, "nil": nil, "invalid": map[string]string{"x": "y"}})
	if value, found, err := request.CookieStrict("number"); value != "42" || !found || err != nil {
		t.Fatalf("scalar override: %q %v %v", value, found, err)
	}
	if value, found, err := request.CookieStrict("nil"); value != "" || !found || err != nil {
		t.Fatalf("explicit nil override: %q %v %v", value, found, err)
	}
	if value, found, err := request.CookieStrict("invalid"); value != "" || found || !errors.Is(err, cookie.ErrInvalidCookieValue) {
		t.Fatalf("invalid override: %q %v %v", value, found, err)
	}
	if request.Cookie("invalid", "fallback") != "FALLBACK" || request.Cookie("uid", "fallback") != "FALLBACK" {
		t.Fatal("legacy invalid scalar/default or full override changed")
	}
	for _, empty := range []*fwcontext.Request{nil, {}} {
		if value, found, err := empty.CookieStrict("uid"); value != "" || found || err != nil || empty.Cookie("uid", "fallback") != "fallback" {
			t.Fatalf("zero/nil request: value=%q found=%v err=%v", value, found, err)
		}
	}
}

func TestSetCookieDoesNotCollapseUnrelatedDuplicates(t *testing.T) {
	raw := httptest.NewRequest(http.MethodGet, "/", nil)
	raw.Header.Set("Cookie", "uid=alice; uid=attacker; uid=third; empty=")
	request := fwcontext.MustNewRequest(raw)
	request.SetCookie("theme", "dark")
	sources, err := request.SourcesFor(fwcontext.SourceSelection{Cookies: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sources.Cookies["uid"], []string{"alice", "attacker", "third"}) {
		t.Fatalf("unrelated SetCookie collapsed duplicate source: %#v", sources.Cookies)
	}
	if !reflect.DeepEqual(sources.Cookies["theme"], []string{"dark"}) || !reflect.DeepEqual(sources.Cookies["empty"], []string{""}) {
		t.Fatalf("single/empty values changed: %#v", sources.Cookies)
	}
}
