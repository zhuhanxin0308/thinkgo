package http

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const protocolReadDeadline = 80 * time.Millisecond

// newHTTP3DeadlineFixture 为每种慢客户端用独立真实连接和证书，避免连接池复用隐藏超时状态。
func newHTTP3DeadlineFixture(t *testing.T, handler http.Handler) (string, *tls.Config, context.CancelFunc, <-chan error) {
	t.Helper()
	certPath, keyPath, tlsConfig := protocolTestCertificate(t)
	config, err := parseServerConfig(map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	config.Host, config.Port = "127.0.0.1", 0
	config.EnableTLS, config.EnableHTTP3 = true, true
	config.CertFile, config.KeyFile = certPath, keyPath
	config.ReadHeaderTimeout, config.ReadTimeout, config.WriteTimeout = protocolReadDeadline, protocolReadDeadline, protocolReadDeadline
	config.ShutdownTimeout = protocolReadDeadline * 2
	address, cancel, done := startHTTP3NetworkServer(t, handler, config)
	return address, tlsConfig, cancel, done
}

// TestHTTP3RejectsSlowHeaders 验证尚未进入 Handler 的 HEADERS 也有读取期限。
func TestHTTP3RejectsSlowHeaders(t *testing.T) {
	address, tlsConfig, _, _ := newHTTP3DeadlineFixture(t, http.NotFoundHandler())
	tlsConfig.NextProtos = []string{http3.NextProtoH3}
	ctx, cancel := context.WithTimeout(context.Background(), protocolTestTimeout)
	defer cancel()
	connection, err := quic.DialAddr(ctx, address, tlsConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseWithError(0, "")
	stream, err := connection.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// HEADERS 帧声明 63 字节，但不发送字段块，模拟建立连接后持续慢发请求头。
	if _, err = stream.Write([]byte{0x01, 0x3f}); err != nil {
		t.Fatal(err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(time.Second))
	started := time.Now()
	_, err = io.ReadAll(stream)
	var streamErr *quic.StreamError
	if !errors.As(err, &streamErr) || time.Since(started) >= time.Second {
		t.Fatalf("慢请求头应由服务端快速重置: elapsed=%s error=%v", time.Since(started), err)
	}
}

// TestHTTP3BoundsSlowRequestBody 验证 body 的持续时间限制不会被 QUIC 活跃包绕过。
func TestHTTP3BoundsSlowRequestBody(t *testing.T) {
	readResult := make(chan error, 1)
	address, tlsConfig, _, _ := newHTTP3DeadlineFixture(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, err := io.ReadAll(request.Body)
		readResult <- err
		writer.WriteHeader(http.StatusRequestTimeout)
	}))
	transport := &http3.Transport{TLSClientConfig: tlsConfig}
	defer transport.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), protocolTestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+address+"/body", reader)
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = 2
	go func() {
		response, _ := (&http.Client{Transport: transport}).Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
	}()
	if _, err := writer.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readResult:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("慢 body 应触发读取截止时间: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("慢 body 没有在配置期限内中断")
	}
}

// TestHTTP3BoundsBlockedResponseWriter 验证客户端不消费响应时写入也有截止时间。
func TestHTTP3BoundsBlockedResponseWriter(t *testing.T) {
	writeResult := make(chan error, 1)
	address, tlsConfig, _, _ := newHTTP3DeadlineFixture(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		block := make([]byte, 1<<20)
		_, err := writer.Write(block)
		writeResult <- err
	}))
	transport := &http3.Transport{
		TLSClientConfig: tlsConfig,
		QUICConfig:      &quic.Config{InitialStreamReceiveWindow: 1024, MaxStreamReceiveWindow: 1024},
	}
	defer transport.Close()
	client := &http.Client{Transport: transport, Timeout: protocolTestTimeout}
	response, err := client.Get("https://" + address + "/blocked")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	select {
	case err := <-writeResult:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("客户端不读取时应命中写期限: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("响应写入没有在配置期限内中断")
	}
}

// TestHTTP3ShutdownReportsUncooperativePeer 验证没有客户端关闭配合时，停机有界且明确报告超时。
func TestHTTP3ShutdownReportsUncooperativePeer(t *testing.T) {
	address, tlsConfig, cancel, done := newHTTP3DeadlineFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	transport := &http3.Transport{TLSClientConfig: tlsConfig}
	defer transport.Close()
	response, err := (&http.Client{Transport: transport, Timeout: protocolTestTimeout}).Get("https://" + address + "/idle")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("保留连接的客户端应得到明确排空超时: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("不合作客户端导致服务器停机无界等待")
	}
}
