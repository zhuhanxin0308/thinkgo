package context

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

// TestResponseSingleHeaderConstructionAllocations 约束普通响应只为响应、稳定身份和共享头状态分配对象。
func TestResponseSingleHeaderConstructionAllocations(t *testing.T) {
	const responseStateAllocations = 3
	cases := map[string]func() *Response{
		"default": func() *Response { return NewResponse() },
		"json_header": func() *Response {
			return NewResponse().Header("Content-Type", "application/json")
		},
		"committed": func() *Response { return NewCommittedResponse(http.StatusAccepted) },
	}
	for name, construct := range cases {
		t.Run(name, func(t *testing.T) {
			allocations := testing.AllocsPerRun(100, func() {
				responseAllocationSink = construct()
			})
			if allocations > responseStateAllocations {
				t.Fatalf("单值响应头构造不得提前创建映射或切片，最多 %d 次，实际 %.2f 次", responseStateAllocations, allocations)
			}
		})
	}
}

// TestResponseHeaderStateKeepsEntityDefaults 验证不同实体入口仍保留既有默认类型和显式空值语义。
func TestResponseHeaderStateKeepsEntityDefaults(t *testing.T) {
	const htmlContentType = "text/html; charset=utf-8"
	cases := []struct {
		name        string
		response    *Response
		contentType string
	}{
		{"default", NewResponse(), htmlContentType},
		{"content", NewResponse().Content("hello"), htmlContentType},
		{"data", NewResponse().Data([]byte("hello")), htmlContentType},
		{"json", NewResponse().Json(struct{ OK bool }{true}), "application/json; charset=utf-8"},
		{"jsonp", NewResponse().Jsonp("callback", true), "application/javascript; charset=utf-8"},
		{"xml", NewResponse().Xml("hello"), "application/xml; charset=utf-8"},
		{"stream_default", NewResponse().Stream(func(io.Writer) error { return nil }), htmlContentType},
		{"stream_zero", (&Response{}).Stream(func(io.Writer) error { return nil }), "application/octet-stream"},
		{"stream_empty", NewResponse().Header("Content-Type", "").Stream(func(io.Writer) error { return nil }), "application/octet-stream"},
		{"chunk", NewResponse().Chunk([][]byte{[]byte("hello")}), htmlContentType},
		{"no_content", NewResponse().NoContent(), htmlContentType},
		{"committed", NewCommittedResponse(http.StatusAccepted), htmlContentType},
		{"content_zero", (&Response{}).Content("hello"), htmlContentType},
		{"content_empty", NewResponse().Header("Content-Type", "").Content("hello"), htmlContentType},
		{"content_custom", NewResponse().Header("Content-Type", "text/plain").Content("hello"), "text/plain"},
		{"empty", NewResponse().Header("Content-Type", ""), ""},
	}
	for _, current := range cases {
		t.Run(current.name, func(t *testing.T) {
			if err := current.response.Error(); err != nil {
				t.Fatal(err)
			}
			if got := current.response.GetHeader("content-type"); got != current.contentType {
				t.Fatalf("单项读取类型错误: %q，预期 %q", got, current.contentType)
			}
			if got := current.response.Headers().Values("Content-Type"); !reflect.DeepEqual(got, []string{current.contentType}) {
				t.Fatalf("完整头部快照类型错误: %#v", got)
			}
		})
	}
}

// TestResponseContentTypeTransitions 保证单值和多值头切换不丢顺序、不保留旧值，也不污染已返回快照。
func TestResponseContentTypeTransitions(t *testing.T) {
	response := NewResponse()
	initial := response.Headers()
	response.AddHeader("content-type", "text/plain").AddHeader("Content-Type", "application/json")
	multiple := response.Headers()
	if got := multiple.Values("Content-Type"); !reflect.DeepEqual(got, []string{"text/html; charset=utf-8", "text/plain", "application/json"}) {
		t.Fatalf("追加 Content-Type 的顺序错误: %#v", got)
	}
	response.Header("CONTENT-TYPE", "replacement").AddHeader("Content-Type", "last")
	if got := response.Headers().Values("Content-Type"); !reflect.DeepEqual(got, []string{"replacement", "last"}) {
		t.Fatalf("覆盖后的 Content-Type 仍保留旧值: %#v", got)
	}
	response.Header(http.Header{"Content-Type": []string{"", "tail"}}).Content("hello")
	if got := response.Headers().Values("Content-Type"); !reflect.DeepEqual(got, []string{"text/html; charset=utf-8"}) {
		t.Fatalf("Content 必须替换空首项和旧多值: %#v", got)
	}
	response.Header(http.Header{"Content-Type": nil})
	if got := response.GetHeader("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("空头值集合应保持已有头部: %q", got)
	}
	if initial.Get("Content-Type") != "text/html; charset=utf-8" || len(multiple.Values("Content-Type")) != 3 {
		t.Fatal("后续响应修改污染了先前快照")
	}
	multiple["Content-Type"][0] = "mutated snapshot"
	if response.GetHeader("Content-Type") != "text/html; charset=utf-8" {
		t.Fatal("外部快照可反向修改响应头")
	}
}

// TestResponseHeaderStateKeepsCopyAndIdentity 验证值复制继续共享响应头，同时独立响应不共享身份和内容。
func TestResponseHeaderStateKeepsCopyAndIdentity(t *testing.T) {
	original := NewResponse()
	copied := *original
	copied.Header("Content-Type", "application/json").Header("X-Trace", "first")
	original.AddHeader("Content-Type", "extra").AddHeader("X-Trace", "second")
	if !reflect.DeepEqual(original.Headers(), copied.Headers()) || original.Identity() != copied.Identity() {
		t.Fatal("值复制后响应头共享或稳定身份契约发生变化")
	}
	independent := NewResponse()
	if independent.Identity() == original.Identity() || independent.GetHeader("Content-Type") != "text/html; charset=utf-8" || independent.GetHeader("X-Trace") != "" {
		t.Fatal("独立响应共享了可变状态或稳定身份")
	}
}

// TestResponseHeaderStatePreservesZeroAndRejectedValues 验证零值、空头集合以及被拒绝输入不会意外生成默认 HTML 头。
func TestResponseHeaderStatePreservesZeroAndRejectedValues(t *testing.T) {
	zero := &Response{}
	if zero.Headers() != nil || zero.GetHeader("Content-Type") != "" || len(zero.GetCookie()) != 0 {
		t.Fatal("零值响应应保持空头部")
	}
	zero.Header("X-Trace", "bad\r\nvalue")
	if !errors.Is(zero.Error(), ErrInvalidResponseHeader) || zero.Headers() == nil || len(zero.Headers()) != 0 {
		t.Fatal("拒绝非法值时必须保持已初始化但没有内容的头集合")
	}
	zero.Header("X-Trace", "safe")
	if zero.GetHeader("Content-Type") != "" || zero.GetHeader("X-Trace") != "safe" {
		t.Fatal("零值响应的非类型头不得隐式生成 HTML 类型")
	}
	response := NewResponse().Header("Content-Type", "bad\r\nvalue")
	if !errors.Is(response.Error(), ErrInvalidResponseHeader) || response.GetHeader("Content-Type") != "text/html; charset=utf-8" {
		t.Fatal("非法 Content-Type 必须保留原有值")
	}
}

// TestResponseHeaderStateSendsIndependentValues 验证发送替换同名底层头，同时不共享响应与写入器的头值切片。
func TestResponseHeaderStateSendsIndependentValues(t *testing.T) {
	response := NewResponse().Header("Content-Type", "application/json").AddHeader("X-Trace", "one").AddHeader("X-Trace", "two")
	writer := httptest.NewRecorder()
	writer.Header().Add("Content-Type", "stale")
	writer.Header().Add("X-Trace", "stale")
	writer.Header().Set("X-External", "preserved")
	if err := response.Data([]byte(`{"ok":true}`)).Send(writer); err != nil {
		t.Fatal(err)
	}
	if got := writer.Header().Values("Content-Type"); !reflect.DeepEqual(got, []string{"application/json"}) {
		t.Fatalf("发送时没有完整替换 Content-Type: %#v", got)
	}
	if got := writer.Header().Values("X-Trace"); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("发送时没有保留多值头: %#v", got)
	}
	writer.Header()["X-Trace"][0] = "writer mutation"
	writer.Header()["Content-Type"][0] = "writer mutation"
	if response.GetHeader("X-Trace") != "one" || response.GetHeader("Content-Type") != "application/json" || writer.Header().Get("X-External") != "preserved" {
		t.Fatal("发送过程改变了头部所有权或覆盖了不相关头")
	}
}

// TestResponseHeaderStateReadOnlyConcurrency 验证只读响应可被并发观察，快照中的修改彼此隔离。
func TestResponseHeaderStateReadOnlyConcurrency(t *testing.T) {
	response := NewResponse().Header("Content-Type", "application/json")
	const readers = 8
	var workers sync.WaitGroup
	workers.Add(readers)
	for range readers {
		go func() {
			defer workers.Done()
			if response.GetHeader("Content-Type") != "application/json" {
				t.Error("并发单项读取丢失响应头")
			}
			snapshot := response.Headers()
			snapshot["Content-Type"][0] = "private snapshot"
			if response.GetHeader("Content-Type") != "application/json" {
				t.Error("并发快照污染了原响应")
			}
		}()
	}
	workers.Wait()
}

// TestResponseHeaderStateMatchesStandardHeader 验证零值和默认响应在连续覆盖、追加与空值操作后仍与标准 Header 一致。
func TestResponseHeaderStateMatchesStandardHeader(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		response := &Response{}
		expected := make(http.Header)
		if initialized {
			response = NewResponse()
			expected.Set("Content-Type", "text/html; charset=utf-8")
		}
		operations := []struct {
			key    string
			value  string
			append bool
		}{
			{"content-type", "", true},
			{"Content-Type", "text/plain", true},
			{"X-Trace", "first", true},
			{"CONTENT-TYPE", "application/json", false},
			{"X-Trace", "second", true},
			{"content-type", "application/xml", true},
			{"x-trace", "", false},
			{"Content-Type", "", false},
			{"X-Trace", "last", true},
			{"Content-Type", "text/html", true},
		}
		for index, operation := range operations {
			if operation.append {
				response.AddHeader(operation.key, operation.value)
				expected.Add(operation.key, operation.value)
			} else {
				response.Header(operation.key, operation.value)
				expected.Set(operation.key, operation.value)
			}
			if actual := response.Headers(); !reflect.DeepEqual(actual, expected) {
				t.Fatalf("初始默认头=%t，第 %d 步头部不一致: 实际=%#v 预期=%#v", initialized, index, actual, expected)
			}
			if actual := response.GetHeader(operation.key); actual != expected.Get(operation.key) {
				t.Fatalf("第 %d 步首值不一致: %q", index, actual)
			}
		}
		writer := httptest.NewRecorder()
		if err := response.Code(http.StatusOK).Send(writer); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(writer.Header(), expected) {
			t.Fatalf("发送后的多值头部不一致: 实际=%#v 预期=%#v", writer.Header(), expected)
		}
	}
}
