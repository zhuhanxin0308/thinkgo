package openapi

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestIssue44SpecificationExtensionsAreOpaque(t *testing.T) {
	// Paths, Responses and Callback objects permit opaque x- extensions.
	// Component and webhook maps instead use arbitrary names for real entries.
	parameter := func(rule string) map[string]any {
		return map[string]any{"schema": map[string]any{"x-thinkgo-validation": rule}}
	}
	pathItem := func(rule string) map[string]any {
		return map[string]any{"parameters": []any{parameter(rule)}}
	}
	response := func(rule string) map[string]any {
		return map[string]any{"headers": map[string]any{"extra": parameter(rule)}}
	}
	opaquePath := pathItem("retain-path")
	opaqueCallback := pathItem("retain-callback")
	opaqueResponse := response("retain-response")
	document := map[string]any{
		"components": map[string]any{
			"pathItems": map[string]any{"x-real-component": pathItem("remove-component")},
			"callbacks": map[string]any{"named": map[string]any{
				"x-extension": opaqueCallback,
				"/notify":     pathItem("remove-component-callback"),
			}},
		},
		"webhooks": map[string]any{"x-real-webhook": pathItem("remove-webhook")},
		"paths": map[string]any{
			"x-extension": opaquePath,
			"/actual": map[string]any{"post": map[string]any{
				"responses": map[string]any{"x-extension": opaqueResponse, "200": response("remove-response")},
				"callbacks": map[string]any{"named": map[string]any{
					"x-extension": opaqueCallback,
					"/notify":     pathItem("remove-callback"),
				}},
			}},
		},
	}
	input, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	output, err := withoutValidationRules(input)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output, []byte("remove-")) {
		t.Fatalf("annotation in an actual named entry retained: %s", output)
	}
	for _, opaque := range []map[string]any{opaquePath, opaqueCallback, opaqueResponse} {
		expected, err := json.Marshal(opaque)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(output, expected) {
			t.Errorf("opaque extension changed: want %s in %s", expected, output)
		}
	}
}
