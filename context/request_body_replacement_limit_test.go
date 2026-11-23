package context

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/iotest"
)

type replacementBodyProbe struct {
	io.Reader
	readBytes int
	closes    int
	closeErr  error
}

func (probe *replacementBodyProbe) Read(buffer []byte) (int, error) {
	n, err := probe.Reader.Read(buffer)
	probe.readBytes += n
	return n, err
}

func (probe *replacementBodyProbe) Close() error {
	probe.closes++
	return probe.closeErr
}

// TestReplacedRawBodyCacheLimit 验证缓存读取只消耗上限及一个探测字节，失败也只关闭正文一次。
func TestReplacedRawBodyCacheLimit(t *testing.T) {
	const limit = 8
	readErr := errors.New("读取替换正文失败")
	closeErr := errors.New("关闭替换正文失败")
	for _, test := range []struct {
		name      string
		source    io.Reader
		closeErr  error
		wantErr   error
		wantBytes int
	}{
		{"恰好达到上限", strings.NewReader("12345678"), nil, nil, limit},
		{"超过一字节", strings.NewReader("123456789"), nil, ErrRequestBodyTooLarge, limit + 1},
		{"停止继续消费", strings.NewReader(strings.Repeat("x", limit*4)), nil, ErrRequestBodyTooLarge, limit + 1},
		{"未提供流末尾", io.MultiReader(strings.NewReader("123456789"), iotest.ErrReader(errors.New("不应继续读取"))), nil, ErrRequestBodyTooLarge, limit + 1},
		{"保留读取错误优先级", failingBodyReader{err: readErr}, closeErr, readErr, 0},
		{"保留关闭错误", strings.NewReader("12345678"), closeErr, closeErr, limit},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("old"))
			request := newRequestForTest(t, raw, WithMaxBodyBytes(limit))
			source := &replacementBodyProbe{Reader: test.source, closeErr: test.closeErr}
			request.Raw().ContentLength = -1
			request.Raw().Body = source
			for range 2 {
				body, err := request.Body()
				if !errors.Is(err, test.wantErr) || !errors.Is(request.BodyReadError(), test.wantErr) {
					t.Fatalf("正文错误 = %v，缓存错误 = %v，期望 %v", err, request.BodyReadError(), test.wantErr)
				}
				if test.wantErr != nil {
					if body != nil || len(request.bodyCache) != 0 || raw.Body != http.NoBody {
						t.Fatal("失败后仍有可见的正文或缓存")
					}
				} else if string(body) != "12345678" {
					t.Fatalf("合法正文未完整恢复: %q", body)
				}
			}
			if source.readBytes != test.wantBytes || source.closes != 1 {
				t.Fatalf("读取 %d 字节，关闭 %d 次，期望读取 %d 字节并关闭一次", source.readBytes, source.closes, test.wantBytes)
			}
		})
	}
}

// TestMultipartLimitCountsOnlyReadEpilogue 验证解析器预读尾声时仍执行硬上限，
// 同时尊重未预读尾声的流式调用方，不为验证大小主动排空请求。
func TestMultipartLimitCountsOnlyReadEpilogue(t *testing.T) {
	const envelope = "--test\r\nContent-Disposition: form-data; name=\"key\"\r\n\r\nv\r\n--test--\r\n"
	const epilogue = "x"
	for _, replace := range []bool{false, true} {
		for _, prefetch := range []bool{false, true} {
			var content io.Reader = strings.NewReader(envelope + epilogue)
			if !prefetch {
				content = io.MultiReader(strings.NewReader(envelope), strings.NewReader(epilogue))
			}
			source := &replacementBodyProbe{Reader: content}
			raw := httptest.NewRequest(http.MethodPost, "/?key=query", source)
			raw.Header.Set("Content-Type", "multipart/form-data; boundary=test")
			request := newRequestForTest(t, raw, WithMaxBodyBytes(int64(len(envelope))))
			request.SetRoute("route", "matched")
			if replace {
				raw.Body = source
			}
			err := request.Parse()
			if prefetch {
				if !errors.Is(err, ErrRequestBodyTooLarge) || source.readBytes != len(envelope)+1 {
					t.Errorf("替换=%v，已预读超限尾声：读取 %d，错误 %v", replace, source.readBytes, err)
				}
				if request.Post("key", "absent") != "absent" || request.Param("key") != "query" ||
					request.All()["key"] != "query" || request.All()["route"] != "matched" {
					t.Errorf("已拒绝表单影响查询或路由值: %v", request.All())
				}
				request.WithPost(map[string]any{"key": "override"})
				if request.Post("key") != "override" || request.All()["key"] != "override" {
					t.Error("已拒绝表单遮蔽了调用方显式提供的值")
				}
			} else if err != nil || source.readBytes != len(envelope) || raw.PostForm.Get("key") != "v" {
				t.Errorf("替换=%v，未预读尾声：读取 %d，表单 %v，错误 %v", replace, source.readBytes, raw.PostForm, err)
			}
			if err := request.Cleanup(); err != nil {
				t.Fatal(err)
			}
			if !prefetch && request.Post("key") != "v" {
				t.Error("清理后已成功解析的普通表单字段丢失")
			}
		}
	}
}

// TestMultipartKeepsEarlierStreamLimit 验证放宽后续解析预算时，已有流的较小上限仍然生效。
func TestMultipartKeepsEarlierStreamLimit(t *testing.T) {
	const envelope = "--test--\r\n"
	raw := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(envelope+"x"))
	raw.ContentLength = -1
	raw.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	request := newRequestForTest(t, raw, WithMaxBodyBytes(int64(len(envelope))))
	if err := WithMaxBodyBytes(int64(len(envelope) + 1))(request); err != nil {
		t.Fatal(err)
	}
	if err := request.Parse(); !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("初始限制器的预读超限未保留: %v", err)
	}
	if err := request.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

// TestRejectedMultipartKeepsTemporaryFileCleanup 验证两个错误均可分类，拒绝数据后仍能清理落盘上传。
func TestRejectedMultipartKeepsTemporaryFileCleanup(t *testing.T) {
	var content bytes.Buffer
	writer := multipart.NewWriter(&content)
	if err := writer.WriteField("body", "rejected"); err != nil {
		t.Fatal(err)
	}
	filePart, err := writer.CreateFormFile("upload", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(filePart, "temporary"); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw := httptest.NewRequest(http.MethodPost, "/?invalid=%zz", nil)
	raw.ContentLength = -1
	raw.Header.Set("Content-Type", writer.FormDataContentType())
	request := newRequestForTest(t, raw, WithMaxBodyBytes(int64(content.Len())), WithMultipartMemoryLimit(1))
	raw.Body = io.NopCloser(strings.NewReader(content.String() + "x"))
	t.Cleanup(func() {
		if err := request.Cleanup(); err != nil {
			t.Error(err)
		}
	})
	err = request.Parse()
	var queryError url.EscapeError
	if !errors.Is(err, ErrRequestBodyTooLarge) || !errors.As(err, &queryError) {
		t.Fatalf("未同时保留大小和查询错误: %v", err)
	}
	if header, err := request.File("upload"); header != nil || !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("被拒绝的上传仍可访问: %v, %v", header, err)
	}
	if raw.MultipartForm == nil || len(raw.MultipartForm.File["upload"]) != 1 {
		t.Fatal("临时文件清理记录缺失")
	}
	header := raw.MultipartForm.File["upload"][0]
	if err := request.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if f, err := header.Open(); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			_ = f.Close()
		}
		t.Fatalf("清理后临时文件仍可访问: %v", err)
	}
	if request.Post("body") != "" || request.All()["body"] != nil {
		t.Fatal("清理后已拒绝的字段重新可见")
	}
}

// TestReplacedRawBodyParsingLimit 验证结构化绑定、表单与不透明正文使用相同的读取预算。
func TestReplacedRawBodyParsingLimit(t *testing.T) {
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	if err := writer.WriteField("field", "value"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, media, body string
		read              func(*Request) error
	}{
		{"JSON 绑定", "application/json", `{"a":true}`, func(r *Request) error {
			var target map[string]any
			return r.Json(&target)
		}},
		{"JSON 预校验", "application/json", `{"a":true}`, (*Request).ValidateBody},
		{"表单", "application/x-www-form-urlencoded", "field=value", (*Request).Parse},
		{"不透明正文", "application/octet-stream", "field=value", (*Request).ValidateBody},
		{"流式表单", writer.FormDataContentType(), form.String(), (*Request).Parse},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := httptest.NewRequest(http.MethodPost, "/", nil)
			raw.Header.Set("Content-Type", test.media)
			limit := len(test.body) / 2
			request := newRequestForTest(t, raw, WithMaxBodyBytes(int64(limit)))
			raw.ContentLength = -1
			source := &replacementBodyProbe{Reader: strings.NewReader(test.body)}
			raw.Body = source
			if err := test.read(request); !errors.Is(err, ErrRequestBodyTooLarge) {
				t.Fatalf("解析错误 = %v，期望请求体超限", err)
			}
			if source.readBytes != limit+1 || len(request.bodyCache) != 0 {
				t.Fatalf("读取 %d 字节，缓存 %d 字节", source.readBytes, len(request.bodyCache))
			}
			if err := request.Cleanup(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
