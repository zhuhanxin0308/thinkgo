package openapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

func TestRegistryRejectsQueryPathsWithoutPublishingContracts(t *testing.T) {
	for _, path := range []string{
		"/foo?bar=1", "/foo?", "/?bar=1", "/foo?bar=1/baz",
		" /foo?bar=1/ ", "/users/:id?", "/users/{id}?bar=1",
	} {
		t.Run(path, func(t *testing.T) {
			registry, err := NewRegistry(openapi3.Info{Title: "API", Version: "1"})
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Register(http.MethodGet, path, validOperation("read")); !errors.Is(err, ErrUnsupportedRoute) {
				t.Fatalf("query path registered: %v", err)
			}
			// A rejected contract must not reserve its operationId or publish a path.
			if err := registry.Register(http.MethodGet, "/safe", validOperation("read")); err != nil {
				t.Fatalf("rejected operation polluted registration state: %v", err)
			}
			requireSingleExportedPath(t, registry, "/safe")
		})
	}
}

func TestRegistryPreservesEscapedPathsAndParameters(t *testing.T) {
	for _, test := range []struct {
		path, want, routerPath, parameter string
	}{
		{path: "/", want: "/", routerPath: "/"},
		{path: " /foo/ ", want: "/foo", routerPath: "/foo"},
		{path: "/foo%3Fbar=1", want: "/foo%3Fbar=1", routerPath: "/foo%3Fbar=1"},
		{path: "/foo%3fbar=1", want: "/foo%3fbar=1", routerPath: "/foo%3fbar=1"},
		{path: "/foo%253Fbar=1", want: "/foo%253Fbar=1", routerPath: "/foo%253Fbar=1"},
		{path: "/foo%23bar", want: "/foo%23bar", routerPath: "/foo%23bar"},
		{path: "/users/:id", want: "/users/{id}", routerPath: "/users/:id", parameter: "id"},
		{path: "/users/{id}", want: "/users/{id}", routerPath: "/users/:id", parameter: "id"},
		{path: "/users/:id/reports%3Fexport", want: "/users/{id}/reports%3Fexport", routerPath: "/users/:id/reports%3Fexport", parameter: "id"},
	} {
		t.Run(test.path, func(t *testing.T) {
			registry, err := NewRegistry(openapi3.Info{Title: "API", Version: "1"})
			if err != nil {
				t.Fatal(err)
			}
			operation := validOperation("read")
			if test.parameter != "" {
				operation.Parameters = openapi3.Parameters{
					{Value: openapi3.NewPathParameter(test.parameter).WithSchema(openapi3.NewStringSchema())},
				}
			}
			if err := registry.Register(http.MethodGet, test.path, operation); err != nil {
				t.Fatal(err)
			}
			router := route.NewRouter()
			if _, err := router.Get(test.routerPath, "User@Show"); err != nil {
				t.Fatal(err)
			}
			if err := registry.ValidateRouter(router, "/"); err != nil {
				t.Fatalf("legal path coverage changed: %v", err)
			}
			requireSingleExportedPath(t, registry, test.want)
		})
	}
}

func requireSingleExportedPath(t *testing.T, registry *Registry, want string) {
	t.Helper()
	content, err := registry.JSON(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Paths) != 1 || document.Paths[want] == nil {
		t.Fatalf("exported paths=%v, want only %q", document.Paths, want)
	}
}
