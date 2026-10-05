package openapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

type issue44Input struct {
	binding.Input
	Name string `json:"name" validate:"required|length:2,32"`
	ID   int64  `json:"id,string" validate:"gt:0"`
}

func TestIssue44ValidationRulesExportOption(t *testing.T) {
	for _, mode := range []string{"default", "enabled", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			var options []RegistryOption
			if mode != "default" {
				options = append(options, WithValidationRules(mode == "enabled"))
			}
			registry, err := NewRegistry(openapi3.Info{Title: "Issue44", Version: "1"}, options...)
			if err != nil {
				t.Fatal(err)
			}
			if err := RegisterTyped[issue44Input, typedResponse](registry, http.MethodPost, "/typed", "typed", http.StatusOK); err != nil {
				t.Fatal(err)
			}
			router := route.NewRouter()
			if err := Handle(router, registry, Operation{Method: http.MethodPost, Path: "/handled", OperationID: "handled"}, func(input issue44Input) typedResponse {
				return typedResponse{Name: input.Name}
			}); err != nil {
				t.Fatal(err)
			}
			before, err := json.Marshal(registry.document)
			if err != nil {
				t.Fatal(err)
			}
			content, err := registry.JSON(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(content), `"x-thinkgo-validation"`) != (mode != "disabled") {
				t.Fatalf("unexpected validation extension for %s: %s", mode, content)
			}
			for _, expected := range []string{`"minLength":2`, `"maxLength":32`, `"contentSchema"`, `"exclusiveMinimum":0`} {
				if !strings.Contains(string(content), expected) {
					t.Errorf("standard constraint missing: %s", expected)
				}
			}
			after, err := json.Marshal(registry.document)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("export modified registered source schemas")
			}
			if err := WithValidationRules(true)(registry); !errors.Is(err, ErrRegistryFrozen) {
				t.Fatalf("frozen registry accepted option: %v", err)
			}
		})
	}
	if err := WithValidationRules(false)(nil); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("nil registry option: %v", err)
	}
}

func TestIssue44SchemaPositionsAndLiteralKeys(t *testing.T) {
	// Opaque data must retain the same key, while actual schema annotations disappear.
	const input = `{
 "openapi":"3.1.0",
 "components":{
  "schemas":{"x-thinkgo-validation":{
   "type":"object", "x-thinkgo-validation":"remove-root",
   "properties":{"x-thinkgo-validation":{"type":"string","x-thinkgo-validation":"remove-property","example":{"x-thinkgo-validation":"retain-example"}}},
   "default":{"x-thinkgo-validation":"retain-default","number":9007199254740993},
   "enum":[{"x-thinkgo-validation":"retain-enum"}],
   "const":{"x-thinkgo-validation":"retain-const"},
   "patternProperties":{"^test":{"x-thinkgo-validation":"remove-pattern"}},
   "$defs":{"defined":{"x-thinkgo-validation":"remove-def"}},
   "definitions":{"legacy":{"x-thinkgo-validation":"remove-legacy"}},
   "dependentSchemas":{"flag":{"x-thinkgo-validation":"remove-dependent"}},
   "allOf":[{"x-thinkgo-validation":"remove-allOf"}],
   "oneOf":[{"x-thinkgo-validation":"remove-oneOf"}],
   "anyOf":[{"x-thinkgo-validation":"remove-anyOf"}],
   "prefixItems":[{"x-thinkgo-validation":"remove-prefix"}],
   "items":[{"x-thinkgo-validation":"remove-items"}],
   "additionalProperties":{"x-thinkgo-validation":"remove-additional"},
   "additionalItems":{"x-thinkgo-validation":"remove-additional-item"},
   "propertyNames":{"x-thinkgo-validation":"remove-names"},
   "unevaluatedProperties":{"x-thinkgo-validation":"remove-unevaluated"},
   "unevaluatedItems":{"x-thinkgo-validation":"remove-unevaluated-item"},
   "not":{"x-thinkgo-validation":"remove-not"},
   "if":{"x-thinkgo-validation":"remove-if"},"then":{"x-thinkgo-validation":"remove-then"},"else":{"x-thinkgo-validation":"remove-else"},
   "contains":{"x-thinkgo-validation":"remove-contains"},
   "contentSchema":{"type":"integer","x-thinkgo-validation":"remove-contentSchema"},
   "x-custom":{"x-thinkgo-validation":"retain-vendor"}
  }},
  "parameters":{"shared":{"schema":{"x-thinkgo-validation":"remove-component-param"}}},
  "headers":{"shared":{"schema":{"x-thinkgo-validation":"remove-component-header"}}},
  "requestBodies":{"shared":{"content":{"application/json":{"schema":{"x-thinkgo-validation":"remove-component-body"}}}}},
  "responses":{"shared":{"content":{"application/json":{"schema":{"x-thinkgo-validation":"remove-component-response"}}}}},
  "callbacks":{"shared":{"/notify":{"post":{"requestBody":{"content":{"application/json":{"schema":{"x-thinkgo-validation":"remove-component-callback"}}}}}}}},
  "pathItems":{"shared":{"parameters":[{"schema":{"x-thinkgo-validation":"remove-component-path"}}]}}
 },
 "paths":{"/sample":{
  "parameters":[{"schema":{"x-thinkgo-validation":"remove-path-param"}}],
  "post":{
   "parameters":[{"schema":{"x-thinkgo-validation":"remove-operation-param"}},{"content":{"application/json":{"schema":{"x-thinkgo-validation":"remove-param-content"}}}}],
   "requestBody":{"content":{"application/json":{"schema":{"items":{"x-thinkgo-validation":"remove-request-items"}},"encoding":{"part":{"headers":{"extra":{"schema":{"x-thinkgo-validation":"remove-encoding"}}}}}}}},
   "responses":{"200":{"headers":{"extra":{"schema":{"x-thinkgo-validation":"remove-header"}}},"content":{"application/json":{"schema":{"$ref":"#/components/schemas/x-thinkgo-validation"},"example":{"x-thinkgo-validation":"retain-response-example"}}}}},
   "callbacks":{"done":{"/notify":{"post":{"requestBody":{"content":{"application/json":{"schema":{"x-thinkgo-validation":"remove-callback"}}}}}}}}
  }
 }},
 "webhooks":{"/hook":{"post":{"responses":{"204":{"headers":{"extra":{"schema":{"x-thinkgo-validation":"remove-webhook"}}}}}}}}
}`
	output, err := withoutValidationRules([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output, []byte("remove-")) {
		t.Fatalf("schema annotation retained: %s", output)
	}
	for _, preserved := range []string{"retain-example", "retain-default", "retain-enum", "retain-const", "retain-vendor", "retain-response-example", "9007199254740993", "#/components/schemas/x-thinkgo-validation"} {
		if !bytes.Contains(output, []byte(preserved)) {
			t.Errorf("opaque data or reference lost: %s", preserved)
		}
	}
	var document map[string]any
	if err := json.Unmarshal(output, &document); err != nil {
		t.Fatal(err)
	}
	schemas := schemaObject(schemaObject(document["components"])["schemas"])
	schema, ok := schemas["x-thinkgo-validation"]
	if !ok || schemaObject(schemaObject(schema)["properties"])["x-thinkgo-validation"] == nil {
		t.Fatal("literal component/property name removed")
	}
	if _, err := withoutValidationRules([]byte(`{`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestIssue44ExportIsolatedConcurrentAndHTTP(t *testing.T) {
	registry, err := NewRegistry(openapi3.Info{Title: "API", Version: "1"}, WithValidationRules(false))
	if err != nil {
		t.Fatal(err)
	}
	schema := openapi3.NewStringSchema()
	schema.Extensions = map[string]any{"x-thinkgo-validation": "private-rule"}
	if err := registry.AddSchema("Manual", &openapi3.SchemaRef{Value: schema}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterTyped[issue44Input, typedResponse](registry, "POST", "/sample", "sample", 200); err != nil {
		t.Fatal(err)
	}
	content, err := registry.JSON(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte("x-thinkgo-validation")) || schema.Extensions["x-thinkgo-validation"] != "private-rule" {
		t.Fatal("manual schema annotation leaked or caller input mutated")
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			independent, err := registry.JSON(context.Background())
			if err != nil || !bytes.Equal(independent, content) {
				t.Errorf("concurrent snapshot mismatch: %v", err)
				return
			}
			independent[0] = ' '
		})
	}
	wg.Wait()
	handler, err := registry.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), content) {
		t.Fatal("HTTP output differs from filtered snapshot")
	}
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/openapi.json", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != get.Header().Get("ETag") {
		t.Fatal("HEAD/ETag changed")
	}
	conditional := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	conditional.Header.Set("If-None-Match", get.Header().Get("ETag"))
	notModified := httptest.NewRecorder()
	handler.ServeHTTP(notModified, conditional)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatal("conditional request changed")
	}
	// Hiding documentation metadata never relaxes the binding plan.
	raw := httptest.NewRequest(http.MethodPost, "http://localhost/sample", strings.NewReader(`{"name":"x","id":"1"}`))
	raw.Header.Set("Content-Type", "application/json")
	var value issue44Input
	if err := binding.Bind(fwcontext.MustNewRequest(raw), &value); err == nil {
		t.Fatal("invalid input accepted after documentation export")
	}
}

func TestIssue44FilteringDoesNotBypassDocumentValidation(t *testing.T) {
	registry, err := NewRegistry(openapi3.Info{Title: "API", Version: "1"}, WithValidationRules(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddSchema("External", &openapi3.SchemaRef{Ref: "https://example.invalid/schema.json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.JSON(context.Background()); !errors.Is(err, ErrInvalidDocument) || registry.frozen {
		t.Fatalf("invalid document frozen or validation bypassed: %v", err)
	}
	for _, value := range []string{`null`, `false`, `true`, `{}`, `{"items":false}`} {
		var schema any
		if err := json.Unmarshal([]byte(value), &schema); err != nil {
			t.Fatal(err)
		}
		omitSchemaValidationRules(schema)
	}
}
