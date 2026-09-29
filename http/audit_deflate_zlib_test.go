package http

import (
	"bytes"
	"compress/zlib"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The independent decoder intentionally uses zlib, not raw compress/flate.
func TestAuditHTTPDeflateMustUseZlibWrapper(t *testing.T) {
	payload := []byte(strings.Repeat("thinkgo-audit-deflate-", 64))
	for _, level := range []int{zlib.HuffmanOnly, zlib.NoCompression, zlib.BestSpeed, zlib.DefaultCompression, zlib.BestCompression} {
		t.Run(strconv.Itoa(level), func(t *testing.T) {
			// Repeated requests exercise Reset after a compressor returns to its pool.
			for attempt := 0; attempt < 3; attempt++ {
				req := httptest.NewRequest(stdhttp.MethodGet, "http://example.test/", nil)
				req.Header.Set("Accept-Encoding", "deflate")
				recorder := httptest.NewRecorder()
				writer := NewCompressionResponseWriter(recorder, req, 1, map[string]int{"deflate": level})
				writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
				half := len(payload) / 2
				if _, err := writer.Write(payload[:half]); err != nil {
					t.Fatal(err)
				}
				if err := writer.flushResponse(); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(payload[half:]); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				result := recorder.Result()
				if result.Header.Get("Content-Encoding") != "deflate" {
					t.Fatalf("wrong encoding: %q", result.Header.Get("Content-Encoding"))
				}
				decoder, err := zlib.NewReader(result.Body)
				if err != nil {
					_ = result.Body.Close()
					t.Fatalf("HTTP deflate is not zlib-wrapped: %v", err)
				}
				decoded, readErr := io.ReadAll(decoder)
				closeErr := decoder.Close()
				bodyErr := result.Body.Close()
				if readErr != nil || closeErr != nil || bodyErr != nil {
					t.Fatalf("decode: read=%v close=%v body=%v", readErr, closeErr, bodyErr)
				}
				if !bytes.Equal(decoded, payload) {
					t.Fatal("decompressed body differs from original")
				}
			}
		})
	}
}
