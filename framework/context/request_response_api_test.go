package context

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestTypedAccessorsAndBatchGetters(t *testing.T) {
	body := `{"score": 95.5, "enabled": true, "nickname": "张三"}`
	raw := httptest.NewRequest(http.MethodPost, "/users/18?page=3&active=true", strings.NewReader(body))
	raw.Header.Set("Content-Type", "application/json")

	req := NewRequest(raw)
	req.Set("id", "18")

	if value := req.Route("id"); value != "18" {
		t.Fatalf("Route 应优先返回路由参数，实际为 %q", value)
	}
	if value := req.ParamInt("page", 1); value != 3 {
		t.Fatalf("ParamInt 应返回查询参数整数值，实际为 %d", value)
	}
	if value := req.ParamFloat("score", 0); value != 95.5 {
		t.Fatalf("ParamFloat 应返回 JSON 中的浮点值，实际为 %v", value)
	}
	if value := req.ParamBool("enabled", false); !value {
		t.Fatal("ParamBool 应返回 JSON 中的布尔值 true")
	}
	if value := req.ParamBool("missing", true); !value {
		t.Fatal("缺失布尔参数时应返回默认值")
	}

	only := req.Only("id", "page", "nickname")
	if only["id"] != "18" {
		t.Fatalf("Only 应保留路由参数，实际为 %#v", only["id"])
	}
	if only["page"] != "3" {
		t.Fatalf("Only 应保留查询参数，实际为 %#v", only["page"])
	}
	if only["nickname"] != "张三" {
		t.Fatalf("Only 应保留 JSON 参数，实际为 %#v", only["nickname"])
	}

	all := req.All()
	if all["id"] != "18" || all["page"] != "3" || all["nickname"] != "张三" {
		t.Fatalf("All 返回结果不完整，实际为 %#v", all)
	}

	except := req.Except("nickname")
	if _, ok := except["nickname"]; ok {
		t.Fatalf("Except 应排除指定字段，实际为 %#v", except)
	}
}

// TestRequestParamPrefersBodyOverQuery 验证状态变更请求中 body 参数优先于 URL query，避免查询串覆盖提交内容。
func TestRequestParamPrefersBodyOverQuery(t *testing.T) {
	body := `{"role":"user"}`
	raw := httptest.NewRequest(http.MethodPost, "/users?role=admin", strings.NewReader(body))
	raw.Header.Set("Content-Type", "application/json")
	req := NewRequest(raw)

	if value := req.Param("role"); value != "user" {
		t.Fatalf("Param 应优先返回 body 中的 role，实际为 %q", value)
	}

	req.Set("role", "owner")
	if value := req.Param("role"); value != "owner" {
		t.Fatalf("路由参数仍应拥有最高优先级，实际为 %q", value)
	}
}

// TestRequestJsonReturnsBodyReadError 验证 JSON 绑定会返回底层请求体读取错误，而不是伪装成空 JSON。
func TestRequestJsonReturnsBodyReadError(t *testing.T) {
	readErr := errors.New("request body read failed")
	raw := httptest.NewRequest(http.MethodPost, "/users", failingBodyReader{err: readErr})
	raw.Header.Set("Content-Type", "application/json")
	req := NewRequest(raw)

	var payload map[string]interface{}
	err := req.Json(&payload)
	if !errors.Is(err, readErr) {
		t.Fatalf("Json 应返回请求体读取错误，实际为 %v", err)
	}
}

type failingBodyReader struct {
	err error
}

func (r failingBodyReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestResponseAdvancedHelpers(t *testing.T) {
	recorder := httptest.NewRecorder()
	resp := NewResponse().Jsonp("callback_1", map[string]interface{}{"ok": true})
	resp.Send(recorder)

	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "application/javascript") {
		t.Fatalf("Jsonp 应返回 application/javascript，实际为 %q", contentType)
	}
	if body := strings.TrimSpace(recorder.Body.String()); body != `callback_1({"ok":true});` {
		t.Fatalf("Jsonp 响应内容不正确，实际为 %q", body)
	}

	noContentRecorder := httptest.NewRecorder()
	NewResponse().NoContent().Send(noContentRecorder)
	if noContentRecorder.Code != http.StatusNoContent {
		t.Fatalf("NoContent 状态码不正确，实际为 %d", noContentRecorder.Code)
	}
	if noContentRecorder.Body.Len() != 0 {
		t.Fatalf("NoContent 不应输出响应体，实际为 %q", noContentRecorder.Body.String())
	}

	streamRecorder := httptest.NewRecorder()
	NewResponse().Stream(func(writer io.Writer) error {
		_, err := writer.Write([]byte("stream-"))
		if err != nil {
			return err
		}
		_, err = writer.Write([]byte("body"))
		return err
	}).Send(streamRecorder)
	if streamRecorder.Body.String() != "stream-body" {
		t.Fatalf("Stream 输出内容不正确，实际为 %q", streamRecorder.Body.String())
	}

	chunkRecorder := httptest.NewRecorder()
	NewResponse().Chunk([][]byte{
		[]byte("chunk"),
		[]byte("-"),
		[]byte("data"),
	}).Send(chunkRecorder)
	if chunkRecorder.Body.String() != "chunk-data" {
		t.Fatalf("Chunk 输出内容不正确，实际为 %q", chunkRecorder.Body.String())
	}

	abortRecorder := httptest.NewRecorder()
	NewResponse().Abort(http.StatusForbidden, map[string]interface{}{"message": "denied"}).Send(abortRecorder)
	if abortRecorder.Code != http.StatusForbidden {
		t.Fatalf("Abort 状态码不正确，实际为 %d", abortRecorder.Code)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(abortRecorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Abort 应输出 JSON，实际错误为 %v", err)
	}
	if payload["message"] != "denied" {
		t.Fatalf("Abort 响应体不正确，实际为 %#v", payload)
	}
}

// TestResponseHeaderRejectsInjection 验证通用 Header API 会阻断响应头注入。
func TestResponseHeaderRejectsInjection(t *testing.T) {
	resp := NewResponse().
		Header("X-Trace", "ok\r\nSet-Cookie: bad=1\x00").
		Header("Bad\r\nName", "value")

	if got := resp.Headers().Get("X-Trace"); strings.ContainsAny(got, "\r\n\x00") {
		t.Fatalf("响应头值不应包含控制字符，实际为 %q", got)
	}
	for key := range resp.Headers() {
		if strings.ContainsAny(key, "\r\n") {
			t.Fatalf("非法响应头名不应被写入，实际存在 %q", key)
		}
	}
}

// TestResponseSerializationErrorIsMasked 验证序列化失败时响应体不会泄露内部错误详情。
func TestResponseSerializationErrorIsMasked(t *testing.T) {
	for name, resp := range map[string]*Response{
		"json":  NewResponse().Json(leakingJSON{}),
		"jsonp": NewResponse().Jsonp("callback", leakingJSON{}),
		"xml":   NewResponse().Xml(leakingXML{}),
	} {
		t.Run(name, func(t *testing.T) {
			if resp.GetStatus() != http.StatusInternalServerError {
				t.Fatalf("序列化失败应返回 500，实际为 %d", resp.GetStatus())
			}
			body := string(resp.GetBody())
			if strings.Contains(body, "secret-token") || strings.Contains(body, "internal encoder detail") {
				t.Fatalf("序列化失败响应不应泄露内部错误，实际为 %q", body)
			}
		})
	}
}

type leakingJSON struct{}

func (leakingJSON) MarshalJSON() ([]byte, error) {
	return nil, errors.New("secret-token: internal encoder detail")
}

type leakingXML struct{}

func (leakingXML) MarshalXML(encoder *xml.Encoder, start xml.StartElement) error {
	return errors.New("secret-token: internal encoder detail")
}
