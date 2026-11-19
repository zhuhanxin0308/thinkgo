package testkit

import (
	"net/http/httptest"
	"testing"
)

// TestDecodeJSONUsesCommittedResponseHeaders 验证解码依据首次提交的响应头，而非后续可变 HeaderMap。
func TestDecodeJSONUsesCommittedResponseHeaders(t *testing.T) {
	tests := []struct {
		name           string
		committedType  string
		mutatedType    string
		writeBody      bool
		wantDecodeErr  bool
	}{
		{
			name:          "提交后从文本改为 JSON 仍拒绝",
			committedType: "text/plain",
			mutatedType:   "application/json",
			wantDecodeErr: true,
		},
		{
			name:          "提交后从 JSON 改为文本仍接受",
			committedType: "application/json",
			mutatedType:   "text/plain",
			writeBody:     true,
		},
		{
			name:          "首次写入隐式提交后修改头部仍接受",
			committedType: "application/problem+json",
			mutatedType:   "text/plain",
			writeBody:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			response.Header().Set("Content-Type", test.committedType)
			if test.writeBody {
				// Write 首次提交响应，同时写入正文。
				if _, err := response.Write([]byte(`{"name":"Ada"}`)); err != nil {
					t.Fatal(err)
				}
			} else {
				response.WriteHeader(200)
				response.Body.WriteString(`{"name":"Ada"}`)
			}
			response.Header().Set("Content-Type", test.mutatedType)
			bodyBefore := response.Body.String()

			value, err := DecodeJSON[map[string]string](response)
			if test.wantDecodeErr {
				if err == nil {
					t.Fatalf("应依据已提交的 %q 拒绝解码，实际值=%#v", test.committedType, value)
				}
			} else if err != nil || value["name"] != "Ada" {
				t.Fatalf("应依据已提交的 %q 成功解码，值=%#v err=%v", test.committedType, value, err)
			}
			if response.Body.String() != bodyBefore {
				t.Fatal("DecodeJSON 不应消耗或修改 ResponseRecorder 原始响应体")
			}
		})
	}
}
