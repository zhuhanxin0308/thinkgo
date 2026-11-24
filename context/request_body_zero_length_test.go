package context

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 顺序替换正文后，过期的零长度元数据不能让表单或二进制正文跳过实际读取限制。
func TestReplacedZeroLengthBodyValidation(t *testing.T) {
	const limit = 4
	for _, media := range []string{"application/x-www-form-urlencoded", "application/octet-stream"} {
		for _, validate := range []bool{false, true} {
			t.Run(media+map[bool]string{false: "/Parse", true: "/ValidateBody"}[validate], func(t *testing.T) {
				raw := httptest.NewRequest(http.MethodPost, "/", nil)
				raw.Header.Set("Content-Type", media)
				request := newRequestForTest(t, raw, WithMaxBodyBytes(limit))
				source := &replacementBodyProbe{Reader: strings.NewReader("key=long")}
				raw.Body = source
				read := (*Request).Parse
				if validate {
					read = (*Request).ValidateBody
				}
				for range 2 {
					if err := read(request); !errors.Is(err, ErrRequestBodyTooLarge) {
						t.Fatalf("零长度元数据掩盖替换流的超限：%v", err)
					}
				}
				if source.readBytes != limit+1 || source.closes != 1 {
					t.Fatalf("超限流读取=%d，关闭=%d", source.readBytes, source.closes)
				}
				if err := request.Cleanup(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// 实际为空的请求保持成功；合法替换流即使没有更新长度也必须正常提供数据。
func TestZeroLengthBodyEmptyAndValidReplacement(t *testing.T) {
	for _, test := range []struct {
		name     string
		body     io.ReadCloser
		wantBody bool
		wantForm string
	}{
		{name: "空指针"},
		{name: "NoBody", body: http.NoBody},
		{name: "空流", body: io.NopCloser(strings.NewReader(""))},
		{name: "合法表单", body: io.NopCloser(strings.NewReader("k=v")), wantBody: true, wantForm: "v"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := httptest.NewRequest(http.MethodPost, "/", nil)
			raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request := newRequestForTest(t, raw, WithMaxBodyBytes(int64(len("k=v"))))
			raw.Body = test.body
			if err := request.Parse(); err != nil {
				t.Fatal(err)
			}
			if raw.PostForm.Get("k") != test.wantForm {
				t.Fatalf("合法替换表单未预解析：%v", raw.PostForm)
			}
			if err := request.ValidateBody(); err != nil {
				t.Fatal(err)
			}
			sources, err := request.Sources()
			if err != nil || sources.HasBody != test.wantBody {
				t.Fatalf("正文存在性错误：HasBody=%t，错误=%v", sources.HasBody, err)
			}
			if err := request.Cleanup(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
