package context

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestTypedAccessorsAndBatchGetters(t *testing.T) {
	body := `{"score": 95.5, "enabled": true, "nickname": "张三"}`
	raw := httptest.NewRequest(http.MethodPost, "/users/18?page=3&active=true", strings.NewReader(body))
	raw.Header.Set("Content-Type", "application/json")

	req := newRequestForTest(t, raw)
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
	req := newRequestForTest(t, raw)

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
	req := newRequestForTest(t, raw)

	var payload map[string]interface{}
	err := req.Json(&payload)
	if !errors.Is(err, readErr) {
		t.Fatalf("Json 应返回请求体读取错误，实际为 %v", err)
	}
}

// TestRequestBodyLimitAndDefensiveCopy 验证请求体有独立硬上限，且调用方不能修改内部缓存。
func TestRequestBodyLimitAndDefensiveCopy(t *testing.T) {
	tooLarge := newRequestForTest(
		t,
		httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("12345")),
		WithMaxBodyBytes(4),
	)
	if _, err := tooLarge.Body(); !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("超限请求体应返回 ErrRequestBodyTooLarge，实际为 %v", err)
	}

	request := newRequestForTest(
		t,
		httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("1234")),
		WithMaxBodyBytes(4),
	)
	first, err := request.Body()
	if err != nil {
		t.Fatalf("读取合法请求体失败: %v", err)
	}
	first[0] = 'x'
	second, err := request.Body()
	if err != nil {
		t.Fatalf("再次读取请求体失败: %v", err)
	}
	if string(second) != "1234" {
		t.Fatalf("Body 必须返回防御性副本，实际为 %q", string(second))
	}
}

// TestRequestRejectsMalformedJSONDocuments 验证语法错误、尾随文档、重复键和非对象根节点都不会被吞掉。
func TestRequestRejectsMalformedJSONDocuments(t *testing.T) {
	tests := map[string]string{
		"语法错误": `{"name":`,
		"尾随文档": `{"name":"alice"} {"admin":true}`,
		"重复键":  `{"role":"user","role":"admin"}`,
		"非对象根": `[1,2,3]`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			raw := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
			raw.Header.Set("Content-Type", "application/json")
			req := newRequestForTest(t, raw)
			if err := req.Parse(); !errors.Is(err, ErrInvalidJSONBody) {
				t.Fatalf("非法 JSON 应返回 ErrInvalidJSONBody，实际为 %v", err)
			}
			if req.Has("role") {
				t.Fatal("解析失败的 JSON 不得暴露部分数据")
			}
		})
	}
}

// TestRequestPreservesExplicitEmptyValues 验证显式空值与字段缺失语义不同。
func TestRequestPreservesExplicitEmptyValues(t *testing.T) {
	raw := httptest.NewRequest(http.MethodPost, "/users?query=", strings.NewReader("form="))
	raw.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req := newRequestForTest(t, raw)

	if got := req.Get("query", "fallback"); got != "" {
		t.Fatalf("显式空查询参数不应替换为默认值，实际为 %q", got)
	}
	if got := req.Post("form", "fallback"); got != "" {
		t.Fatalf("显式空表单参数不应替换为默认值，实际为 %q", got)
	}
	if got := req.Post("missing", "fallback"); got != "fallback" {
		t.Fatalf("缺失字段应返回默认值，实际为 %q", got)
	}
}

// TestRequestTypedAccessorsRejectLossyValues 验证类型转换不会截断小数、溢出整数或接受非有限浮点数。
func TestRequestTypedAccessorsRejectLossyValues(t *testing.T) {
	req := newRequestForTest(t, httptest.NewRequest(http.MethodGet, "/", nil))
	req.Set("fraction", 1.25)
	req.Set("overflow", uint64(math.MaxUint64))
	req.Set("nan", math.NaN())
	req.Set("numeric_bool", 2)
	req.Set("panic_stringer", panickingStringer{})

	if got := req.ParamInt("fraction", 7); got != 7 {
		t.Fatalf("小数不能被截断为整数，实际为 %d", got)
	}
	if got := req.ParamInt64("overflow", 9); got != 9 {
		t.Fatalf("溢出整数必须回退默认值，实际为 %d", got)
	}
	if got := req.ParamFloat("nan", 2.5); got != 2.5 {
		t.Fatalf("NaN 必须回退默认值，实际为 %v", got)
	}
	if got := req.ParamBool("numeric_bool", false); got {
		t.Fatalf("非 0/1 数字不能被隐式转换，实际为 %v", got)
	}
	if got := req.Param("panic_stringer", "safe"); got != "safe" {
		t.Fatalf("不受支持的对象不能调用 String 方法，实际为 %q", got)
	}
}

// TestRequestAllReturnsDeepCopies 验证批量读取不会泄露 JSON 缓存中的可变嵌套对象。
func TestRequestAllReturnsDeepCopies(t *testing.T) {
	raw := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"profile":{"roles":["user"]}}`))
	raw.Header.Set("Content-Type", "Application/JSON; Charset=UTF-8")
	req := newRequestForTest(t, raw)

	first := req.All()
	profile, ok := first["profile"].(map[string]interface{})
	if !ok {
		t.Fatalf("profile 类型错误: %#v", first["profile"])
	}
	roles, ok := profile["roles"].([]interface{})
	if !ok || len(roles) != 1 {
		t.Fatalf("roles 类型错误: %#v", profile["roles"])
	}
	roles[0] = "admin"

	second := req.All()
	secondProfile := second["profile"].(map[string]interface{})
	secondRoles := secondProfile["roles"].([]interface{})
	if secondRoles[0] != "user" {
		t.Fatalf("All 返回值修改污染了内部缓存: %#v", secondRoles)
	}
}

// TestRequestParseValidatesOpaqueBodies 验证非 JSON/表单请求体也会在业务边界前完成大小校验并缓存。
func TestRequestParseValidatesOpaqueBodies(t *testing.T) {
	raw := httptest.NewRequest(http.MethodPost, "/binary", strings.NewReader("12345"))
	raw.ContentLength = -1
	raw.Header.Set("Content-Type", "application/octet-stream")
	req := newRequestForTest(t, raw, WithMaxBodyBytes(4))
	if err := req.Parse(); !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("不透明请求体超限应返回 ErrRequestBodyTooLarge，实际为 %v", err)
	}
}

// TestRequestAbsoluteURLsRejectInvalidHost 验证绝对地址辅助方法不会把非法 Host 写入链接。
func TestRequestAbsoluteURLsRejectInvalidHost(t *testing.T) {
	for _, host := range []string{
		"trusted.example@evil.example",
		"example.com:not-a-port",
		"example.com:65536",
		"[[::1]]",
		"::1",
	} {
		raw := httptest.NewRequest(http.MethodGet, "http://example.com/users?page=1", nil)
		raw.Host = host
		req := newRequestForTest(t, raw)
		if req.Root() != "" || req.BaseUrl() != "" || req.Url(true) != "" || req.Port() != "" {
			t.Fatalf("非法 Host %q 不得生成绝对地址或端口，root=%q base=%q url=%q port=%q", host, req.Root(), req.BaseUrl(), req.Url(true), req.Port())
		}
	}
}

type panickingStringer struct{}

func (panickingStringer) String() string {
	panic("不应调用不受信对象的 String 方法")
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
	if err := resp.Send(recorder); err != nil {
		t.Fatalf("发送 JSONP 响应失败: %v", err)
	}

	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "application/javascript") {
		t.Fatalf("Jsonp 应返回 application/javascript，实际为 %q", contentType)
	}
	if body := strings.TrimSpace(recorder.Body.String()); body != `callback_1({"ok":true});` {
		t.Fatalf("Jsonp 响应内容不正确，实际为 %q", body)
	}

	noContentRecorder := httptest.NewRecorder()
	if err := NewResponse().NoContent().Send(noContentRecorder); err != nil {
		t.Fatalf("发送空响应失败: %v", err)
	}
	if noContentRecorder.Code != http.StatusNoContent {
		t.Fatalf("NoContent 状态码不正确，实际为 %d", noContentRecorder.Code)
	}
	if noContentRecorder.Body.Len() != 0 {
		t.Fatalf("NoContent 不应输出响应体，实际为 %q", noContentRecorder.Body.String())
	}

	streamRecorder := httptest.NewRecorder()
	if err := NewResponse().Stream(func(writer io.Writer) error {
		_, err := writer.Write([]byte("stream-"))
		if err != nil {
			return err
		}
		_, err = writer.Write([]byte("body"))
		return err
	}).Send(streamRecorder); err != nil {
		t.Fatalf("发送流式响应失败: %v", err)
	}
	if streamRecorder.Body.String() != "stream-body" {
		t.Fatalf("Stream 输出内容不正确，实际为 %q", streamRecorder.Body.String())
	}

	chunkRecorder := httptest.NewRecorder()
	if err := NewResponse().Chunk([][]byte{
		[]byte("chunk"),
		[]byte("-"),
		[]byte("data"),
	}).Send(chunkRecorder); err != nil {
		t.Fatalf("发送分块响应失败: %v", err)
	}
	if chunkRecorder.Body.String() != "chunk-data" {
		t.Fatalf("Chunk 输出内容不正确，实际为 %q", chunkRecorder.Body.String())
	}

	abortRecorder := httptest.NewRecorder()
	if err := NewResponse().Abort(http.StatusForbidden, map[string]interface{}{"message": "denied"}).Send(abortRecorder); err != nil {
		t.Fatalf("发送终止响应失败: %v", err)
	}
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
		Header("Bad\r\nName", "value").
		Header("Transfer-Encoding", "chunked").
		Header("Content-Length", "999")

	if got := resp.Headers().Get("X-Trace"); got != "" {
		t.Fatalf("包含控制字符的响应头值应被整体拒绝，实际为 %q", got)
	}
	for key := range resp.Headers() {
		if strings.ContainsAny(key, "\r\n") {
			t.Fatalf("非法响应头名不应被写入，实际存在 %q", key)
		}
	}
	if resp.Headers().Get("Transfer-Encoding") != "" || resp.Headers().Get("Content-Length") != "" {
		t.Fatalf("实体分帧响应头必须由 HTTP 内核管理: %#v", resp.Headers())
	}
}

// TestResponseRejectsInvalidControlMetadata 验证非法状态码、JSONP 回调和重定向不会进入底层 HTTP 写入器。
func TestResponseRejectsInvalidControlMetadata(t *testing.T) {
	statusRecorder := httptest.NewRecorder()
	statusResponse := NewResponse().Code(99).Content("private body")
	if err := statusResponse.Send(statusRecorder); !errors.Is(err, ErrInvalidResponseStatus) {
		t.Fatalf("非法状态码应返回 ErrInvalidResponseStatus，实际为 %v", err)
	}
	if statusRecorder.Code != http.StatusInternalServerError || strings.Contains(statusRecorder.Body.String(), "private body") {
		t.Fatalf("非法状态码必须安全降级为通用 500，status=%d body=%q", statusRecorder.Code, statusRecorder.Body.String())
	}

	jsonpRecorder := httptest.NewRecorder()
	jsonpResponse := NewResponse().Jsonp("call back", map[string]bool{"ok": true})
	if err := jsonpResponse.Send(jsonpRecorder); err != nil {
		t.Fatalf("非法 JSONP 回调应生成可发送的 400 响应: %v", err)
	}
	if jsonpRecorder.Code != http.StatusBadRequest || strings.Contains(jsonpRecorder.Body.String(), "call back(") {
		t.Fatalf("包含空白的 JSONP 回调不得被静默改写，status=%d body=%q", jsonpRecorder.Code, jsonpRecorder.Body.String())
	}

	redirectRecorder := httptest.NewRecorder()
	redirectResponse := NewResponse().Redirect("/safe\r\nX-Test: injected", http.StatusNotModified)
	if err := redirectResponse.Send(redirectRecorder); !errors.Is(err, ErrInvalidRedirect) {
		t.Fatalf("非法重定向应返回 ErrInvalidRedirect，实际为 %v", err)
	}
	if redirectRecorder.Code != http.StatusInternalServerError || redirectRecorder.Header().Get("Location") != "" {
		t.Fatalf("非法重定向不得写出 Location，status=%d location=%q", redirectRecorder.Code, redirectRecorder.Header().Get("Location"))
	}
}

type responseFlushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (r *responseFlushRecorder) Flush() {
	r.flushes++
	r.ResponseRecorder.Flush()
}

// TestResponseSourceReplacementAndSnapshots 验证响应实体来源互斥、分块及时刷新，且读取快照不能反向修改响应。
func TestResponseSourceReplacementAndSnapshots(t *testing.T) {
	streamCalled := false
	response := NewResponse().
		Stream(func(io.Writer) error {
			streamCalled = true
			return nil
		}).
		Abort(http.StatusForbidden, nil)
	recorder := httptest.NewRecorder()
	if err := response.Send(recorder); err != nil {
		t.Fatalf("发送终止响应失败: %v", err)
	}
	if streamCalled || recorder.Body.Len() != 0 || recorder.Code != http.StatusForbidden {
		t.Fatalf("Abort 必须清除旧流来源，called=%v status=%d body=%q", streamCalled, recorder.Code, recorder.Body.String())
	}

	chunkRecorder := &responseFlushRecorder{ResponseRecorder: httptest.NewRecorder()}
	chunkResponse := NewResponse().Chunk([][]byte{[]byte("one"), []byte("two")})
	if err := chunkResponse.Send(chunkRecorder); err != nil {
		t.Fatalf("发送分块响应失败: %v", err)
	}
	if chunkRecorder.flushes != 2 || chunkRecorder.Header().Get("Transfer-Encoding") != "" {
		t.Fatalf("分块响应应逐块刷新且不手工写入 hop-by-hop 头，flushes=%d transfer=%q", chunkRecorder.flushes, chunkRecorder.Header().Get("Transfer-Encoding"))
	}

	snapshotResponse := NewResponse().Content("safe").Header("X-Test", "original")
	body := snapshotResponse.GetBody()
	body[0] = 'X'
	headers := snapshotResponse.Headers()
	headers.Set("X-Test", "changed")
	if string(snapshotResponse.GetBody()) != "safe" || snapshotResponse.Headers().Get("X-Test") != "original" {
		t.Fatal("响应体和响应头访问器必须返回防御性副本")
	}

	noContent := NewResponse().Chunk([][]byte{[]byte("old")}).NoContent()
	if noContent.Headers().Get("Transfer-Encoding") != "" {
		t.Fatal("NoContent 必须清理旧实体的传输编码")
	}
}

// TestResponseSendReturnsStreamError 验证流式回调错误向上传播且不会把内部错误文本追加到部分响应。
func TestResponseSendReturnsStreamError(t *testing.T) {
	streamErr := errors.New("private stream failure")
	recorder := httptest.NewRecorder()
	err := NewResponse().Stream(func(io.Writer) error {
		return streamErr
	}).Send(recorder)
	if !errors.Is(err, streamErr) {
		t.Fatalf("流式错误应原样向上传播，实际为 %v", err)
	}
	if strings.Contains(recorder.Body.String(), streamErr.Error()) || strings.Contains(recorder.Body.String(), internalServerErrorMessage) {
		t.Fatalf("流式错误不得写入客户端响应体: %q", recorder.Body.String())
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

	recorder := httptest.NewRecorder()
	chained := NewResponse().Json(leakingJSON{}).Content("private fallback body")
	if err := chained.Send(recorder); !errors.Is(err, ErrResponseSerialization) {
		t.Fatalf("序列化失败应保留 ErrResponseSerialization，实际为 %v", err)
	}
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "private fallback body") {
		t.Fatalf("序列化失败后不得通过链式 Content 覆盖通用 500，状态=%d，响应=%q", recorder.Code, recorder.Body.String())
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

type shortResponseWriter struct {
	header http.Header
	status int
}

func (w *shortResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *shortResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *shortResponseWriter) Write(body []byte) (int, error) {
	if len(body) == 0 {
		return 0, nil
	}
	return len(body) - 1, nil
}

// TestResponseMetadataAPIs 验证重复响应头、Cookie、内容类型和缓存元数据均按标准格式输出并拒绝非法值。
func TestResponseMetadataAPIs(t *testing.T) {
	timestamp := time.Date(2026, time.July, 11, 8, 30, 0, 0, time.UTC)
	timestampText := timestamp.Format(http.TimeFormat)
	response := NewResponse().
		AddHeader("X-Trace", "first").
		AddHeader("X-Trace", "second").
		Cookie("session", "safe", 3600, "/", "example.com", true, true).
		ContentType("application/json", "utf-8").
		Expires(timestampText).
		LastModified(timestampText).
		CacheControl("private, max-age=60").
		ETag(`"revision-1"`)

	headers := response.Headers()
	if values := headers.Values("X-Trace"); len(values) != 2 || values[0] != "first" || values[1] != "second" {
		t.Fatalf("重复响应头输出错误: %#v", values)
	}
	if cookie := headers.Get("Set-Cookie"); !strings.Contains(cookie, "session=safe") || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "Secure") {
		t.Fatalf("Cookie 输出错误: %q", cookie)
	}
	if headers.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type 输出错误: %q", headers.Get("Content-Type"))
	}
	if headers.Get("Expires") != timestamp.Format(http.TimeFormat) || headers.Get("Last-Modified") != timestamp.Format(http.TimeFormat) {
		t.Fatalf("缓存时间响应头错误: expires=%q modified=%q", headers.Get("Expires"), headers.Get("Last-Modified"))
	}
	if headers.Get("Cache-Control") != "private, max-age=60" || headers.Get("ETag") != `"revision-1"` {
		t.Fatalf("缓存控制响应头错误: %#v", headers)
	}

	invalid := NewResponse().ContentType("bad content type").Cookie("bad\nname", "value", 0, "/", "", false, true)
	if !errors.Is(invalid.Error(), ErrInvalidResponseHeader) {
		t.Fatalf("非法内容类型和 Cookie 应记录 ErrInvalidResponseHeader，实际为 %v", invalid.Error())
	}

	redirect := NewResponse().Redirect("/next", http.StatusTemporaryRedirect)
	if redirect.GetStatus() != http.StatusTemporaryRedirect || redirect.Headers().Get("Location") != "/next" {
		t.Fatalf("合法重定向构建错误: status=%d location=%q", redirect.GetStatus(), redirect.Headers().Get("Location"))
	}
}

// TestResponseSendDetectsShortWrite 验证底层静默短写会转换为 io.ErrShortWrite，避免把截断响应误判为成功。
func TestResponseSendDetectsShortWrite(t *testing.T) {
	writer := &shortResponseWriter{}
	if err := NewResponse().Content("complete-body").Send(writer); !errors.Is(err, io.ErrShortWrite) || !IsResponseTransmissionError(err) {
		t.Fatalf("响应短写应标记为传输错误并返回 io.ErrShortWrite，实际为 %v", err)
	}
	streamWriter := &shortResponseWriter{}
	streamResponse := NewResponse().Stream(func(writer io.Writer) error {
		_, _ = writer.Write([]byte("complete-stream"))
		return nil
	})
	if err := streamResponse.Send(streamWriter); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("即使回调忽略写结果，流式响应也应返回 io.ErrShortWrite，实际为 %v", err)
	}
}

// TestResponseNoBodyStatusesAndTypedNilWriter 验证 205 不输出实体，且接口中的空写入器不会触发二次 panic。
func TestResponseNoBodyStatusesAndTypedNilWriter(t *testing.T) {
	recorder := httptest.NewRecorder()
	if err := NewResponse().Code(http.StatusResetContent).Content("must-not-send").Send(recorder); err != nil {
		t.Fatalf("发送 205 响应失败: %v", err)
	}
	if recorder.Code != http.StatusResetContent || recorder.Body.Len() != 0 {
		t.Fatalf("205 响应不得包含实体，状态=%d，响应=%q", recorder.Code, recorder.Body.String())
	}

	var typedNil *shortResponseWriter
	if err := NewResponse().Content("body").Send(typedNil); !errors.Is(err, ErrInvalidResponseWriter) {
		t.Fatalf("类型化空写入器应返回 ErrInvalidResponseWriter，实际为 %v", err)
	}
}
