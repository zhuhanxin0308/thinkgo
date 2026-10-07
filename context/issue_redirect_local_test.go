package context

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestIssue32RedirectLocalPreservesLocalTargets(t *testing.T) {
	targets := []string{"/", "/account", "/a/b", "/a//b", "/search?q=hello%20world&next=https%3A%2F%2Fexample.org", "/文档", "/a%20b", "/discount/100%25", "/account#settings"}
	for _, target := range targets {
		for _, status := range []int{301, 302, 303, 307, 308} {
			response := NewResponse().RedirectLocal(target, status)
			writer := httptest.NewRecorder()
			if err := response.Send(writer); err != nil {
				t.Fatal(err)
			}
			if writer.Code != status || writer.Header().Get("Location") != target {
				t.Fatalf("%q: status=%d Location=%q", target, writer.Code, writer.Header().Get("Location"))
			}
		}
	}
	if got := NewResponse().RedirectLocal("/").GetStatus(); got != http.StatusFound {
		t.Fatal(got)
	}
	var zero Response
	if got := zero.RedirectLocal("/ok"); got != &zero || got.GetHeader("Location") != "/ok" {
		t.Fatal("zero value rejected")
	}
	var absent *Response
	if absent.RedirectLocal("/ok") != nil {
		t.Fatal("nil receiver changed")
	}
	// The existing API deliberately still supports trusted external destinations.
	if err := NewResponse().Redirect("https://example.org/login").Error(); err != nil {
		t.Fatal(err)
	}
}

func TestIssue32RedirectLocalRejectsUnsafeTargets(t *testing.T) {
	targets := []string{
		"", "account", "?next=/ok", "#local", "https://example.org", "javascript:alert(1)",
		"//example.org", "///example.org", "//user@example.org", `/\example.org`, `\example.org`,
		" /ok", "/ok ", "/\t/evil", "/ok\r\nLocation: https://example.org", "/ok\x7f", "/\u0085evil",
		"/%2fexample.org", "/%2Fexample.org", "/%5cexample.org", "/%5Cexample.org", "/%00", "/%0d%0a",
		"/a/../next", "/./next", "/a/%2e%2e/next", "/%2e/next", "/a/%2E./next", "/a%2f..%2fnext",
		"/%", "/%GG", "/%ff", "/\xff",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			executed := false
			response := NewResponse().Header("Location", "https://stale.invalid").Stream(func(w io.Writer) error {
				executed = true
				_, err := io.WriteString(w, "private payload")
				return err
			}).RedirectLocal(target)
			writer := httptest.NewRecorder()
			if err := response.Send(writer); !errors.Is(err, ErrInvalidRedirect) {
				t.Fatalf("target %q err=%v", target, err)
			}
			if response.GetHeader("Location") != "" || writer.Header().Get("Location") != "" ||
				writer.Code != http.StatusInternalServerError || executed || strings.Contains(writer.Body.String(), "private payload") {
				t.Fatalf("invalid target caused a redirect or entity send: %+v", writer)
			}
			if strings.Contains(response.Error().Error(), "stale.invalid") {
				t.Fatal("error leaked previous destination")
			}
		})
	}
}

func TestIssue32RedirectLocalRejectsInvalidStatus(t *testing.T) {
	for _, codes := range [][]int{{200}, {304}, {500}, {302, 303}} {
		response := NewResponse().Header("Location", "/stale").RedirectLocal("/safe", codes...)
		writer := httptest.NewRecorder()
		if err := response.Send(writer); !errors.Is(err, ErrInvalidRedirect) ||
			writer.Code != 500 || writer.Header().Get("Location") != "" || response.GetHeader("Location") != "" {
			t.Fatalf("codes=%v err=%v writer=%+v", codes, err, writer)
		}
	}
}

func FuzzIssue32RedirectLocal(f *testing.F) {
	for _, seed := range []string{"/", "//evil", "/%2fevil", "/a/..//evil", "/path?q=%2f%2f", "/文档#段落", "/%"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, target string) {
		response := NewResponse().RedirectLocal(target)
		if response.Error() != nil {
			if !errors.Is(response.Error(), ErrInvalidRedirect) || response.GetHeader("Location") != "" {
				t.Fatal("invalid result")
			}
			return
		}
		location := response.GetHeader("Location")
		if !strings.HasPrefix(location, "/") || strings.HasPrefix(location, "//") || location != target {
			t.Fatalf("not a root-relative location: %q", location)
		}
		parsed, err := url.Parse(location)
		if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" {
			t.Fatalf("external URL: %q", location)
		}
		resolved := (&url.URL{Scheme: "https", Host: "origin.invalid", Path: "/base/"}).ResolveReference(parsed)
		if resolved.Scheme != "https" || resolved.Host != "origin.invalid" {
			t.Fatalf("origin changed: %q", resolved)
		}
	})
}
