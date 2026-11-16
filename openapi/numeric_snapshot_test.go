package openapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestRegistryPreservesExactJSONNumbersAcrossPublicInputs(t *testing.T) {
	for _, options := range [][]RegistryOption{nil, {WithValidationRules(false)}} {
		extension := map[string]any{"sequence": uint64(18446744073709551615)}
		registry, err := NewRegistry(openapi3.Info{
			Title: "Numeric contract", Version: "1", Extensions: map[string]any{"x-fixture": extension},
		}, options...)
		if err != nil {
			t.Fatal(err)
		}
		schema := openapi3.NewIntegerSchema()
		schema.Default = int64(9007199254740993)
		schema.Example = int64(9223372036854775807)
		schema.Enum = []any{schema.Default, schema.Example}
		if err := registry.AddSchema("RecordID", &openapi3.SchemaRef{Value: schema}); err != nil {
			t.Fatal(err)
		}
		media := &openapi3.MediaType{Example: map[string]any{"stored": json.Number("9007199254740993")}}
		response := openapi3.NewResponse().WithDescription("ok")
		response.Content = openapi3.Content{"application/json": media}
		operation := &openapi3.Operation{OperationID: "records.read", Responses: openapi3.NewResponses()}
		operation.Responses.Set("200", &openapi3.ResponseRef{Value: response})
		if err := registry.Register(http.MethodGet, "/records", operation); err != nil {
			t.Fatal(err)
		}
		extension["sequence"] = 1
		schema.Default = 2
		media.Example.(map[string]any)["stored"] = 3

		document, err := registry.JSON(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range []string{
			`"sequence":18446744073709551615`,
			`"default":9007199254740993`,
			`"example":9223372036854775807`,
			`"enum":[9007199254740993,9223372036854775807]`,
			`"stored":9007199254740993`,
		} {
			if !strings.Contains(string(document), fragment) {
				t.Fatalf("OpenAPI 精确数值丢失 %s: %s", fragment, document)
			}
		}
	}
}
