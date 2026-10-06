package route

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func TestIssue17DefaultRouterRequiresExplicitRoutes(t *testing.T) {
	for _, frozen := range []bool{false, true} {
		router := NewRouter()
		if frozen {
			if err := router.Freeze(); err != nil {
				t.Fatal(err)
			}
		}
		if router.AutoRouteEnabled() {
			t.Errorf("frozen=%v: implicit dispatch enabled by default", frozen)
		}
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodTrace} {
			for _, path := range []string{"/", "/user/delete", "/admin/user/remove"} {
				req := fwcontext.MustNewRequest(httptest.NewRequest(method, "http://example.com"+path, nil))
				matched, params, err := router.Match(req)
				if err != nil || matched != nil || len(params) != 0 {
					t.Errorf("frozen=%v %s %s: unexpected fallback: route=%v params=%v err=%v", frozen, method, path, matched, params, err)
				}
			}
		}
		if err := router.EnableAutoRoute(true); !errors.Is(err, ErrRouterFrozen) {
			t.Fatalf("frozen policy changed: %v", err)
		}
	}
}

func TestIssue17MethodMissNeverBecomesAutomaticDispatch(t *testing.T) {
	router := NewRouter()
	registered, err := router.Post("/user/delete", "user/delete")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		request := fwcontext.MustNewRequest(httptest.NewRequest(method, "http://example.com/user/delete", nil))
		matched, _, err := router.Match(request)
		if err != nil || matched != nil {
			t.Errorf("%s reached an unregistered method: route=%v err=%v", method, matched, err)
		}
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodPost, "http://example.com/user/delete", nil))
	matched, _, err := router.Match(request)
	if err != nil || matched != registered || matched.IsAuto() {
		t.Fatalf("explicit POST changed: route=%v err=%v", matched, err)
	}
}
