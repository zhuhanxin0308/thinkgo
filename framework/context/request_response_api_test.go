package context

import (
	"encoding/json"
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
