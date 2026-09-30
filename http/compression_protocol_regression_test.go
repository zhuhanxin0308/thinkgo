package http

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 使用独立的协议解码器，同时覆盖 Flush、Close 和池化 Reset 后的下一次请求。
func TestCompressionDeflateUsesZlibWrapper(t *testing.T) {
	for _, level := range []int{zlib.HuffmanOnly, zlib.DefaultCompression, zlib.NoCompression, zlib.BestSpeed, zlib.BestCompression} {
		t.Run(fmt.Sprint(level), func(t *testing.T) {
			for requestNumber := 0; requestNumber < 2; requestNumber++ {
				payload := []byte(strings.Repeat(fmt.Sprintf("request-%d-", requestNumber), 64))
				raw := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/", nil)
				raw.Header.Set("Accept-Encoding", "deflate")
				recorder := httptest.NewRecorder()
				writer := NewCompressionResponseWriter(recorder, raw, 1, map[string]int{"deflate": level})
				writer.Header().Set("Content-Type", "text/plain")
				if _, err := writer.Write(payload[:len(payload)/2]); err != nil {
					t.Fatal(err)
				}
				if err := writer.flushResponse(); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(payload[len(payload)/2:]); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				if got := recorder.Result().Header.Get("Content-Encoding"); got != "deflate" {
					t.Fatalf("Content-Encoding = %q; want deflate", got)
				}
				decoder, err := zlib.NewReader(bytes.NewReader(recorder.Body.Bytes()))
				if err != nil {
					t.Fatalf("HTTP deflate must have a zlib wrapper: %v", err)
				}
				decoded, readErr := io.ReadAll(decoder)
				closeErr := decoder.Close()
				if readErr != nil || closeErr != nil || !bytes.Equal(decoded, payload) {
					t.Fatalf("round trip failed: read=%v close=%v match=%t", readErr, closeErr, bytes.Equal(decoded, payload))
				}
			}
		})
	}
}

// 首次 Write 必须确定隐式 200，不得因缓冲阈值、编码或空写入而改变。
func TestCompressionImplicitStatusMatchesNetHTTP(t *testing.T) {
	for _, encoding := range []string{"", "gzip", "deflate"} {
		for _, size := range []int{0, 1, 64, 128} {
			t.Run(fmt.Sprintf("encoding=%s/size=%d", encoding, size), func(t *testing.T) {
				payload := []byte(strings.Repeat("x", size))
				baseline := httptest.NewRecorder()
				_, _ = baseline.Write(payload)
				baseline.WriteHeader(stdhttp.StatusNotFound)

				raw := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/", nil)
				raw.Header.Set("Accept-Encoding", encoding)
				recorder := httptest.NewRecorder()
				writer := NewCompressionResponseWriter(recorder, raw, 64, nil)
				if _, err := writer.Write(payload); err != nil {
					t.Fatal(err)
				}
				writer.WriteHeader(stdhttp.StatusNotFound)
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				if got := recorder.Result().StatusCode; got != baseline.Result().StatusCode {
					t.Fatalf("status = %d; net/http = %d", got, baseline.Result().StatusCode)
				}
				var reader io.Reader = bytes.NewReader(recorder.Body.Bytes())
				var closer io.Closer
				switch recorder.Result().Header.Get("Content-Encoding") {
				case "gzip":
					decoded, err := gzip.NewReader(reader)
					if err != nil {
						t.Fatal(err)
					}
					reader, closer = decoded, decoded
				case "deflate":
					decoded, err := zlib.NewReader(reader)
					if err != nil {
						t.Fatal(err)
					}
					reader, closer = decoded, decoded
				}
				if closer != nil {
					defer closer.Close()
				}
				decoded, err := io.ReadAll(reader)
				if err != nil || !bytes.Equal(decoded, payload) {
					t.Fatalf("body mismatch: %v", err)
				}
			})
		}
	}
}

func TestCompressionImplicitStatusCanBeResetBeforePhysicalCommit(t *testing.T) {
	recorder := httptest.NewRecorder()
	raw := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/", nil)
	writer := NewCompressionResponseWriter(recorder, raw, 1024, nil)
	if _, err := writer.Write([]byte("stale")); err != nil {
		t.Fatal(err)
	}
	if !writer.ResetUncommitted() {
		t.Fatal("buffered response must remain recoverable")
	}
	writer.WriteHeader(stdhttp.StatusInternalServerError)
	if _, err := writer.Write([]byte("error")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if recorder.Result().StatusCode != stdhttp.StatusInternalServerError || recorder.Body.String() != "error" {
		t.Fatalf("recovery = %d %q", recorder.Result().StatusCode, recorder.Body.String())
	}
}

func TestCompressionFlushLocksImplicitStatus(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := NewCompressionResponseWriter(recorder, nil, 1024, nil)
	if err := writer.flushResponse(); err != nil {
		t.Fatal(err)
	}
	writer.WriteHeader(stdhttp.StatusNotFound)
	if writer.statusCode != stdhttp.StatusOK {
		t.Fatalf("late WriteHeader changed flushed status to %d", writer.statusCode)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}
