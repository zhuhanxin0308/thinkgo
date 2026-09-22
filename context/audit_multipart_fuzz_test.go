package context

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// FuzzMultipartLifecycle 将任意字节装入真实 multipart 文件，核对内容、拒绝上限和清理后访问边界。
func FuzzMultipartLifecycle(f *testing.F) {
	f.Add([]byte("audit"), false)
	f.Add([]byte("\x00\xff--boundary"), true)
	f.Fuzz(func(t *testing.T, payload []byte, truncated bool) {
		const limit = 4096
		if len(payload) > 2*limit {
			return
		}
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "audit.bin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		encoded := body.Bytes()
		if truncated {
			encoded = encoded[:len(encoded)/2]
		}
		raw := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(encoded))
		raw.Header.Set("Content-Type", writer.FormDataContentType())
		request := MustNewRequest(raw, WithMaxBodyBytes(limit), WithMultipartMemoryLimit(1))
		file, err := request.File("file")
		if !truncated && len(encoded) <= limit {
			if err != nil || file == nil {
				t.Fatalf("合法上传被拒绝: %v", err)
			}
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			actual, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || !bytes.Equal(actual, payload) {
				t.Fatal("上传内容损坏")
			}
		} else if err == nil {
			t.Fatal("截断或超限上传被接受")
		}
		if err := request.Cleanup(); err != nil {
			t.Fatal(err)
		}
		if err := request.Cleanup(); err != nil {
			t.Fatal(err)
		}
		if _, err := request.File("file"); !errors.Is(err, ErrRequestCleaned) {
			t.Fatalf("清理后文件仍可访问: %v", err)
		}
		if file != nil && file.Size > 1 {
			if reader, err := file.Open(); err == nil {
				_ = reader.Close()
				t.Fatal("上传临时文件未删除")
			}
		}
	})
}
