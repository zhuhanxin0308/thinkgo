package http

import (
	"bufio"
	stdcontext "context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

const protocolTestTimeout = 5 * time.Second

// protocolTestCertificate 生成仅限本次回环测试使用的证书，并由客户端显式信任。
func protocolTestCertificate(t *testing.T) (string, string, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certPath, keyPath := filepath.Join(directory, "cert.pem"), filepath.Join(directory, "key.pem")
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(parsed)
	return certPath, keyPath, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}
}

// startHTTP3NetworkServer 使用框架真实的 TCP/UDP 宿主，启动通知只传递已经绑定的地址。
func startHTTP3NetworkServer(t *testing.T, handler http.Handler, config serverConf) (string, stdcontext.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := stdcontext.WithCancel(stdcontext.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	completed := make(chan struct{})
	go func() {
		defer close(completed)
		done <- runHTTPServersContext(ctx, handler, config, nil, func(address string) { ready <- address })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-completed:
		case <-time.After(protocolTestTimeout * 2):
			t.Error("协议测试服务器未释放监听资源")
		}
	})
	select {
	case address := <-ready:
		return address, cancel, done
	case err := <-done:
		t.Fatalf("协议测试服务器启动失败: %v", err)
	case <-time.After(protocolTestTimeout):
		t.Fatal("协议测试服务器启动超时")
	}
	return "", cancel, done
}

// TestHTTP3FrameworkStreamFailureAborts 验证框架传输失败会传递成真实 HTTP/3 流重置。
func TestHTTP3FrameworkStreamFailureAborts(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	_, err := mustHTTPRoute(t, app).Get("/failure", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Stream(func(writer io.Writer) error {
			_, _ = io.WriteString(writer, "partial\n")
			if err := writer.(interface{ FlushError() error }).FlushError(); err != nil {
				return err
			}
			return errors.New("upstream failed")
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	address, tlsConfig, _, _ := newHTTP3DeadlineFixture(t, newTestHTTPHandler(t, app))
	transport := &http3.Transport{TLSClientConfig: tlsConfig}
	defer transport.Close()
	response, err := (&http.Client{Transport: transport, Timeout: protocolTestTimeout}).Get("https://" + address + "/failure")
	if err == nil {
		defer response.Body.Close()
		_, err = io.ReadAll(response.Body)
	}
	// 重置可能先于首个数据包到达，但必须是协议层的远端错误，不能把连接失败当作通过。
	var protocolErr *http3.Error
	if !errors.As(err, &protocolErr) || !protocolErr.Remote || protocolErr.ErrorCode != http3.ErrCodeInternalError {
		t.Fatalf("失败的 HTTP/3 流必须收到远端内部错误重置: %v", err)
	}
}

// TestHTTP3ClientCancellationReleasesScope 验证客户端取消会贯穿流回调并归还作用域与任务租约。
func TestHTTP3ClientCancellationReleasesScope(t *testing.T) {
	app := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	closer := &signalingRequestScopedCloser{closed: make(chan struct{})}
	if err := app.BindScoped("cancel.probe", func() interface{} { return closer }); err != nil {
		t.Fatal(err)
	}
	_, err := mustHTTPRoute(t, app).Get("/cancel", func(request *fwcontext.Request) *fwcontext.Response {
		if _, err := request.Make("cancel.probe"); err != nil {
			return fwcontext.NewResponse().Code(http.StatusInternalServerError)
		}
		return fwcontext.NewResponse().Stream(func(writer io.Writer) error {
			_, _ = io.WriteString(writer, "first\n")
			if err := writer.(interface{ FlushError() error }).FlushError(); err != nil {
				return err
			}
			<-request.Context().Done()
			return request.Context().Err()
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	address, tlsConfig, _, _ := newHTTP3DeadlineFixture(t, newTestHTTPHandler(t, app))
	transport := &http3.Transport{TLSClientConfig: tlsConfig}
	defer transport.Close()
	ctx, cancel := stdcontext.WithCancel(stdcontext.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+address+"/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Transport: transport, Timeout: protocolTestTimeout}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err = bufio.NewReader(response.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-closer.closed:
	case <-time.After(time.Second):
		t.Fatal("客户端取消后 Scoped 服务没有关闭")
	}
	waitContext, stop := stdcontext.WithTimeout(stdcontext.Background(), time.Second)
	defer stop()
	if err := app.WaitRequestTasks(waitContext); err != nil {
		t.Fatal(err)
	}
}

// TestHTTP3ShutdownDrainsActiveResponse 验证停止监听不会先破坏已有 QUIC 响应。
func TestHTTP3ShutdownDrainsActiveResponse(t *testing.T) {
	certPath, keyPath, tlsConfig := protocolTestCertificate(t)
	config, err := parseServerConfig(map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	config.Host, config.Port = "127.0.0.1", 0
	config.EnableTLS, config.EnableHTTP3 = true, true
	config.CertFile, config.KeyFile = certPath, keyPath
	config.ShutdownTimeout = protocolTestTimeout
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	address, cancel, serverDone := startHTTP3NetworkServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "first\n")
		_ = http.NewResponseController(writer).Flush()
		select {
		case <-release:
			_, _ = io.WriteString(writer, "final\n")
		case <-request.Context().Done():
		}
	}), config)
	transport := &http3.Transport{TLSClientConfig: tlsConfig}
	defer transport.Close()
	client := &http.Client{Transport: transport, Timeout: protocolTestTimeout}
	response, err := client.Get("https://" + address + "/drain")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	if first, err := reader.ReadString('\n'); err != nil || first != "first\n" {
		t.Fatalf("首段响应错误: %q %v", first, err)
	}
	cancel()
	// 延迟释放应用处理器，确保取消发生时确有尚未完成的业务响应。
	timer := time.AfterFunc(100*time.Millisecond, finish)
	defer timer.Stop()
	remaining, err := io.ReadAll(reader)
	if err != nil || string(remaining) != "final\n" {
		t.Fatalf("优雅关闭截断在途响应: body=%q error=%v", remaining, err)
	}
	_ = transport.Close()
	select {
	case err := <-serverDone:
		if !errors.Is(err, stdcontext.Canceled) {
			t.Fatalf("服务器退出原因错误: %v", err)
		}
	case <-time.After(protocolTestTimeout):
		t.Fatal("在途请求完成后服务器没有退出")
	}
}
