package middleware

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
)

// issue43OldToken independently models the former MAC, never a production fallback.
func issue43OldToken(name, nonce, secret string, now time.Time) string {
	timestamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(name + "\x00v1\x00" + nonce + "\x00" + timestamp))
	return "v1." + nonce + "." + timestamp + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func issue43Submit(t *testing.T, handler Handler, config CSRFConfig, method, token string, form bool, wantStatus int) {
	t.Helper()
	raw := httptest.NewRequest(method, "https://example.com/save", nil)
	if form {
		raw = httptest.NewRequest(method, "https://example.com/save", strings.NewReader(url.Values{config.FieldName: {token}}.Encode()))
		raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		raw.Header.Set(config.HeaderName, token)
	}
	raw.Header.Set("Origin", "https://example.com")
	raw.AddCookie(&http.Cookie{Name: config.CookieName, Value: token})
	called := false
	response := handler(fwcontext.MustNewRequest(raw), func(*fwcontext.Request) *fwcontext.Response {
		called = true
		return fwcontext.NewResponse().Content("accepted")
	})
	if response == nil || response.GetStatus() != wantStatus || called != (wantStatus == http.StatusOK) {
		t.Fatalf("status/callback mismatch: response=%v called=%t want=%d", response, called, wantStatus)
	}
}

func TestIssue43CookieAndCSRFSignaturesAreNotInterchangeable(t *testing.T) {
	secret := strings.Repeat("k", 32)
	config := cookie.DefaultConfig()
	config.Secret = secret
	factory, err := cookie.NewCookieWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	const name = DefaultCSRFCookieName
	value := strings.Repeat("n", 32)
	header, err := factory.BuildHeader(name, value)
	if err != nil {
		t.Fatal(err)
	}
	cookies := (&http.Response{Header: http.Header{"Set-Cookie": {header}}}).Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing signed cookie")
	}
	raw := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	raw.AddCookie(cookies[0])
	reader, err := factory.ForRequest(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, found, err := reader.Get(name); err != nil || !found || got != value {
		t.Fatalf("genuine Cookie round trip failed: found=%t err=%v", found, err)
	}
	for _, token := range []string{cookies[0].Value, "csrf-v1." + strings.TrimPrefix(cookies[0].Value, "v1.")} {
		if _, err := verifyCSRFToken(name, token, secret, 60, time.Now()); !errors.Is(err, ErrInvalidCSRFToken) {
			t.Errorf("Cookie MAC accepted as CSRF, including version relabeling: %v", err)
		}
	}
	nonce := base64.RawURLEncoding.EncodeToString([]byte(value))
	signedCSRF, err := signCSRFToken(name, nonce, secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{signedCSRF, "v1." + strings.TrimPrefix(signedCSRF, "csrf-v1.")} {
		request := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
		request.AddCookie(&http.Cookie{Name: name, Value: token})
		reader, err := factory.ForRequest(request, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, found, err := reader.Get(name); found || !errors.Is(err, cookie.ErrInvalidCookieSignature) {
			t.Errorf("CSRF MAC accepted as Cookie, including version relabeling: found=%t err=%v", found, err)
		}
	}
}

func TestIssue43CSRFDomainAndFieldBinding(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	secret := strings.Repeat("k", 32)
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte("n"), 32))
	token, err := signCSRFToken(DefaultCSRFCookieName, nonce, secret, now)
	if err != nil {
		t.Fatal(err)
	}
	// Independent HMAC-SHA256 reference vector, not generated with the signer under test.
	const expected = "csrf-v1.bm5ubm5ubm5ubm5ubm5ubm5ubm5ubm5ubm5ubm5ubm4.1700000000.rMVzOsJy4j2f0dCgaipLiCM4Q6Q8GQcXDezdCNvS99o"
	if token != expected {
		t.Errorf("CSRF signing domain or version differs from reference vector")
	}
	if got, err := verifyCSRFToken(DefaultCSRFCookieName, token, secret, 60, now); err != nil || got != nonce {
		t.Fatalf("new CSRF token failed round trip: %v", err)
	}
	parts := strings.Split(token, ".")
	for index := range parts {
		changed := append([]string(nil), parts...)
		switch index {
		case 0:
			changed[0] = "v1"
		case 1:
			changed[1] = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte("x"), 32))
		case 2:
			changed[2] = "1700000001"
		case 3:
			changed[3] = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte("x"), sha256.Size))
		}
		if _, err := verifyCSRFToken(DefaultCSRFCookieName, strings.Join(changed, "."), secret, 60, now); !errors.Is(err, ErrInvalidCSRFToken) {
			t.Errorf("tampered field %d accepted: %v", index, err)
		}
	}
	if _, err := verifyCSRFToken("other_csrf", token, secret, 60, now); !errors.Is(err, ErrInvalidCSRFToken) {
		t.Errorf("wrong name accepted: %v", err)
	}
	if _, err := verifyCSRFToken(DefaultCSRFCookieName, token, strings.Repeat("z", 32), 60, now); !errors.Is(err, ErrInvalidCSRFToken) {
		t.Errorf("wrong key accepted: %v", err)
	}
}

func TestIssue43UnsafeRequestsRejectOldSignatures(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("k", 32)
	handler := newCSRFHandler(t, config)
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte("n"), 32))
	old := issue43OldToken(config.CookieName, nonce, config.Secret, time.Now())
	current := issueCSRFTokenCookie(t, handler).Value
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, form := range []bool{false, true} {
			// net/http parses URL-encoded bodies for POST, PUT and PATCH, not DELETE.
			if form && method == http.MethodDelete {
				continue
			}
			carrier := "header"
			if form {
				carrier = "form"
			}
			t.Run(method+"/"+carrier, func(t *testing.T) {
				issue43Submit(t, handler, config, method, old, form, http.StatusForbidden)
				issue43Submit(t, handler, config, method, "csrf-v1."+strings.TrimPrefix(old, "v1."), form, http.StatusForbidden)
				issue43Submit(t, handler, config, method, current, form, http.StatusOK)
			})
		}
	}
}

func TestIssue43SafeRequestsReplaceOldTokens(t *testing.T) {
	config := DefaultCSRFConfig()
	config.Secret = strings.Repeat("k", 32)
	handler := newCSRFHandler(t, config)
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte("n"), 32))
	old := issue43OldToken(config.CookieName, nonce, config.Secret, time.Now())
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			raw := httptest.NewRequest(method, "https://example.com/", nil)
			raw.AddCookie(&http.Cookie{Name: config.CookieName, Value: old})
			response := handler(fwcontext.MustNewRequest(raw), func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse().Content("ok") })
			cookies := responseCookies(response)
			if len(cookies) != 1 || cookies[0].Value == old || !strings.HasPrefix(cookies[0].Value, "csrf-v1.") {
				t.Fatal("safe request did not replace rejected token with current format")
			}
			issue43Submit(t, handler, config, http.MethodPost, cookies[0].Value, false, http.StatusOK)
		})
	}
}

func FuzzIssue43CSRFRejectsForeignMAC(f *testing.F) {
	f.Add([]byte("sample nonce material"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, input []byte) {
		sum := sha256.Sum256(input)
		nonce := base64.RawURLEncoding.EncodeToString(sum[:])
		secret := strings.Repeat("k", 32)
		now := time.Unix(1_700_000_000, 0)
		old := issue43OldToken(DefaultCSRFCookieName, nonce, secret, now)
		for _, token := range []string{old, "csrf-v1." + strings.TrimPrefix(old, "v1.")} {
			if _, err := verifyCSRFToken(DefaultCSRFCookieName, token, secret, 60, now); !errors.Is(err, ErrInvalidCSRFToken) {
				t.Fatal("foreign MAC accepted")
			}
		}
	})
}
