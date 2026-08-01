package http

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"thinkgo/framework"
	fwcontext "thinkgo/framework/context"
	"thinkgo/framework/route"
)

func TestMultiHttpDispatchesApplicationsAndPreservesOriginalRequest(t *testing.T) {
	manager := newMultiHTTPTestManager(t, map[string]interface{}{
		"domain_bind": map[string]interface{}{"admin.example.com": "admin"},
	})
	host, err := NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建统一 HTTP 宿主失败: %v", err)
	}

	raw := httptest.NewRequest(http.MethodPost, "http://example.com/admin/echo?trace=1", strings.NewReader("payload"))
	raw.Header.Set("X-Trace", "original")
	raw.RemoteAddr = "192.0.2.10:4567"
	raw.TLS = &tls.ConnectionState{}
	originalBody := raw.Body
	originalURL := *raw.URL
	originalRequestURI := raw.RequestURI
	originalHost := raw.Host
	originalRemoteAddr := raw.RemoteAddr

	recorder := httptest.NewRecorder()
	host.ServeHTTP(recorder, raw)

	if recorder.Code != http.StatusOK || recorder.Body.String() != "admin|/echo|payload|true" {
		t.Fatalf("应用请求分发结果错误，status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if raw.URL.Path != originalURL.Path || raw.URL.RawQuery != originalURL.RawQuery || raw.RequestURI != originalRequestURI || raw.Host != originalHost || raw.RemoteAddr != originalRemoteAddr || raw.Header.Get("X-Trace") != "original" || raw.TLS == nil {
		t.Fatalf("分发请求不能修改原始请求对象")
	}
	if raw.Body != originalBody {
		t.Fatal("分发流程不能替换原始请求体流")
	}
}

type recordingReadCloser struct {
	lock       sync.Mutex
	reader     *bytes.Reader
	readErr    error
	readCalls  int
	closeCalls int
	consumed   []byte
}

func newRecordingReadCloser(content string) *recordingReadCloser {
	return &recordingReadCloser{reader: bytes.NewReader([]byte(content))}
}

func (body *recordingReadCloser) Read(buffer []byte) (int, error) {
	body.lock.Lock()
	defer body.lock.Unlock()
	body.readCalls++
	if body.readErr != nil {
		return 0, body.readErr
	}
	read, err := body.reader.Read(buffer)
	body.consumed = append(body.consumed, buffer[:read]...)
	return read, err
}

func (body *recordingReadCloser) Close() error {
	body.lock.Lock()
	body.closeCalls++
	body.lock.Unlock()
	return nil
}

func (body *recordingReadCloser) snapshot() (int, int, []byte) {
	body.lock.Lock()
	defer body.lock.Unlock()
	return body.readCalls, body.closeCalls, append([]byte(nil), body.consumed...)
}

func TestCloneApplicationRequestDoesNotReadBody(t *testing.T) {
	readErr := errors.New("请求体不应在克隆时读取")
	body := newRecordingReadCloser("payload")
	body.readErr = readErr
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/admin/echo", body)
	raw.Body = body
	resolution := framework.ApplicationResolution{RewrittenPath: "/echo"}

	cloned, err := cloneApplicationRequest(raw, resolution)
	if err != nil {
		t.Fatalf("克隆请求不应读取请求体: %v", err)
	}
	readCalls, closeCalls, consumed := body.snapshot()
	if readCalls != 0 || closeCalls != 0 || len(consumed) != 0 {
		t.Fatalf("克隆过程不能消费或关闭请求体: reads=%d closes=%d consumed=%q", readCalls, closeCalls, consumed)
	}
	if cloned.Body != raw.Body || cloned.Body != body {
		t.Fatal("克隆请求必须与原请求持有同一个一次性流")
	}
	if cloned.GetBody != nil {
		t.Fatal("一次性请求体不能暴露可重复读取的 GetBody")
	}
}

func TestMultiHttpRejectsUnknownLengthOversizedBodyWith413(t *testing.T) {
	const maxBodyBytes = 8
	manager := newMultiHTTPTestManager(t, map[string]interface{}{
		"server": map[string]interface{}{"host": "127.0.0.1", "port": 18080, "max_body_bytes": maxBodyBytes},
	})
	host, err := NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建统一 HTTP 宿主失败: %v", err)
	}
	body := newRecordingReadCloser("0123456789abcdef")
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/admin/echo", body)
	raw.Body = body
	raw.ContentLength = -1

	recorder := httptest.NewRecorder()
	host.ServeHTTP(recorder, raw)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("未知长度超限请求应返回 413，实际为 %d，body=%q", recorder.Code, recorder.Body.String())
	}
	_, _, consumed := body.snapshot()
	if len(consumed) > maxBodyBytes+1 {
		t.Fatalf("目标应用确认超限后不应继续预读请求体，实际读取 %d 字节", len(consumed))
	}
}

func TestMultiHttpForwardsSmallBodyExactlyOnce(t *testing.T) {
	manager := newMultiHTTPTestManager(t, nil)
	host, err := NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建统一 HTTP 宿主失败: %v", err)
	}
	body := newRecordingReadCloser("small-payload")
	raw := httptest.NewRequest(http.MethodPost, "http://example.com/admin/echo", body)
	raw.Body = body
	raw.ContentLength = -1

	recorder := httptest.NewRecorder()
	host.ServeHTTP(recorder, raw)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "admin|/echo|small-payload|false" {
		t.Fatalf("小请求体转发错误，status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	_, closeCalls, consumed := body.snapshot()
	if !bytes.Equal(consumed, []byte("small-payload")) {
		t.Fatalf("底层流必须按原字节消费一次，实际为 %q", consumed)
	}
	if closeCalls != 1 {
		t.Fatalf("底层流应关闭一次，实际为 %d 次", closeCalls)
	}
	if raw.Body != body {
		t.Fatal("统一宿主不能替换调用方持有的请求体流")
	}
}

func TestMultiHttpKeepsConcurrentRequestBodiesIsolated(t *testing.T) {
	manager := newMultiHTTPTestManager(t, nil)
	host, err := NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建统一 HTTP 宿主失败: %v", err)
	}

	const requestCount = 24
	start := make(chan struct{})
	errorsFound := make(chan error, requestCount)
	var waitGroup sync.WaitGroup
	for index := 0; index < requestCount; index++ {
		index := index
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			application := "index"
			path := "/index/echo"
			if index%2 == 0 {
				application = "admin"
				path = "/admin/echo"
			}
			payload := fmt.Sprintf("stream-%02d-%s", index, application)
			body := newRecordingReadCloser(payload)
			raw := httptest.NewRequest(http.MethodPost, "http://example.com"+path, body)
			raw.Body = body
			raw.ContentLength = -1
			recorder := httptest.NewRecorder()
			host.ServeHTTP(recorder, raw)
			want := application + "|/echo|" + payload + "|false"
			if recorder.Code != http.StatusOK || recorder.Body.String() != want {
				errorsFound <- fmt.Errorf("请求 %d 串流: status=%d want=%q got=%q", index, recorder.Code, want, recorder.Body.String())
				return
			}
			_, closeCalls, consumed := body.snapshot()
			if !bytes.Equal(consumed, []byte(payload)) || closeCalls != 1 || raw.Body != body {
				errorsFound <- fmt.Errorf("请求 %d 流语义错误: consumed=%q closes=%d replaced=%t", index, consumed, closeCalls, raw.Body != body)
			}
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errorsFound)
	for requestErr := range errorsFound {
		t.Error(requestErr)
	}
}

func TestMultiHttpUsesDomainBindingWithoutPathPrefix(t *testing.T) {
	manager := newMultiHTTPTestManager(t, map[string]interface{}{
		"domain_bind": map[string]interface{}{"admin.example.com": "admin"},
	})
	host, err := NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建统一 HTTP 宿主失败: %v", err)
	}

	recorder := httptest.NewRecorder()
	host.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://admin.example.com/users", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "admin|/users|" {
		t.Fatalf("域名绑定应用分发结果错误，status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestMultiHttpReturnsNotFoundForUnknownAndDeniedApplications(t *testing.T) {
	manager := newMultiHTTPTestManager(t, map[string]interface{}{
		"deny_app_list": []interface{}{"admin"},
	})
	host, err := NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建统一 HTTP 宿主失败: %v", err)
	}

	for _, path := range []string{"/admin/users", "/missing/users"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			host.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("应用 %q 应返回 404，实际为 %d", path, recorder.Code)
			}
			if strings.Contains(recorder.Body.String(), "admin|") {
				t.Fatal("被拒绝或未知应用不能进入应用处理器")
			}
		})
	}
}

func TestMultiHttpRunUsesOneHostRunnerAndClosesApplications(t *testing.T) {
	manager := newMultiHTTPTestManager(t, nil)
	host, err := NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建统一 HTTP 宿主失败: %v", err)
	}

	runCount := 0
	host.runHost = func() error {
		runCount++
		return nil
	}
	if err = host.Run(); err != nil {
		t.Fatalf("统一 HTTP 宿主运行失败: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("统一 HTTP 宿主应该只运行一次底层监听器，实际为 %d 次", runCount)
	}
	if err := manager.Close(); err != nil {
		t.Fatal("统一 HTTP 宿主退出后应该完成应用关闭")
	}
}

func TestMultiHttpRunUsesApplicationManagerLifecycle(t *testing.T) {
	manager := newMultiHTTPTestManager(t, nil)
	host, err := NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建统一 HTTP 宿主失败: %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	host.runHost = func() error {
		close(started)
		<-release
		return nil
	}
	runResult := make(chan error, 1)
	var releaseOnce sync.Once
	releaseHost := func() {
		releaseOnce.Do(func() { close(release) })
	}
	t.Cleanup(releaseHost)
	go func() {
		runResult <- host.Run()
	}()
	<-started

	if err = manager.Run(applicationManagerKernelFunc(func() error { return nil })); !errors.Is(err, framework.ErrApplicationManagerRunning) {
		t.Fatalf("统一宿主运行期间管理器必须拒绝重复 Run，实际为 %v", err)
	}
	if err = manager.Close(); !errors.Is(err, framework.ErrApplicationManagerRunning) {
		t.Fatalf("统一宿主运行期间管理器必须拒绝 Close，实际为 %v", err)
	}
	if err = host.Run(); !errors.Is(err, ErrMultiHTTPRunning) {
		t.Fatalf("统一宿主必须拒绝重复 Run，实际为 %v", err)
	}
	releaseHost()
	if err = <-runResult; err != nil {
		t.Fatalf("统一 HTTP 宿主退出失败: %v", err)
	}
}

type applicationManagerKernelFunc func() error

func (kernel applicationManagerKernelFunc) Run() error {
	return kernel()
}

func TestMultiHttpRunServesHealthRouteOverTCP(t *testing.T) {
	manager := newMultiHTTPTestManager(t, nil)
	host, err := NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建统一 HTTP 宿主失败: %v", err)
	}
	host.runHost = func() error {
		server := httptest.NewServer(host)
		response, requestErr := server.Client().Get(server.URL + "/index/health")
		if requestErr != nil {
			server.Close()
			return fmt.Errorf("访问本地健康路由失败: %w", requestErr)
		}
		content, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		server.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		if response.StatusCode != http.StatusOK || string(content) != "index|healthy" {
			return fmt.Errorf("健康路由响应错误: status=%d body=%q", response.StatusCode, content)
		}
		return nil
	}

	if err = host.Run(); err != nil {
		t.Fatalf("真实 TCP 健康检查失败: %v", err)
	}
}

type multiHTTPBootRouteProvider struct{}

func (provider *multiHTTPBootRouteProvider) Register(*framework.App) error {
	return nil
}

func (provider *multiHTTPBootRouteProvider) Boot(app *framework.App) error {
	router, err := framework.ResolveServiceAs[*route.Router](app, framework.ServiceRoute)
	if err != nil {
		return err
	}
	_, err = router.Get("/boot-health", func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("boot-route-ready")
	})
	return err
}

func TestMultiHttpRunFreezesRoutesAfterProviderBoot(t *testing.T) {
	provider := &multiHTTPBootRouteProvider{}
	definitions := []framework.ApplicationDefinition{{
		Name: "index",
		Path: "app/index",
		Register: func(app *framework.App) error {
			return app.RegisterProvider(provider)
		},
	}}
	manager := newMultiHTTPTestManagerWithDefinitions(t, nil, definitions)
	host, err := NewMultiHttp(manager)
	if err != nil {
		t.Fatalf("创建统一 HTTP 宿主失败: %v", err)
	}
	host.runHost = func() error {
		recorder := httptest.NewRecorder()
		host.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://example.com/index/boot-health", nil))
		if recorder.Code != http.StatusOK || recorder.Body.String() != "boot-route-ready" {
			return fmt.Errorf("Provider Boot 路由未命中: status=%d body=%q", recorder.Code, recorder.Body.String())
		}
		return nil
	}

	if err = host.Run(); err != nil {
		t.Fatalf("Provider Boot 后冻结路由失败: %v", err)
	}
}

func newMultiHTTPTestManager(t testing.TB, settings map[string]interface{}) *framework.ApplicationManager {
	definitions := []framework.ApplicationDefinition{
		{Name: "index", Path: "app/index", Register: multiHTTPTestApplicationRegister()},
		{Name: "admin", Path: "app/admin", Register: multiHTTPTestApplicationRegister()},
	}
	return newMultiHTTPTestManagerWithDefinitions(t, settings, definitions)
}

func newMultiHTTPTestManagerWithDefinitions(t testing.TB, settings map[string]interface{}, definitions []framework.ApplicationDefinition) *framework.ApplicationManager {
	t.Helper()
	basePath := t.TempDir()
	for _, directory := range []string{
		filepath.Join(basePath, "config"),
		filepath.Join(basePath, "app", "index", "lang"),
		filepath.Join(basePath, "app", "admin", "lang"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("创建测试目录失败: %v", err)
		}
	}

	appConfig := map[string]interface{}{
		"app_env":     "test",
		"app_name":    "MultiHttpTest",
		"app_debug":   false,
		"default_app": "index",
		"app_express": false,
		"server":      map[string]interface{}{"host": "127.0.0.1", "port": 18080},
		"compression": map[string]interface{}{"enable": false},
	}
	for key, value := range settings {
		appConfig[key] = value
	}
	writeMultiHTTPTestJSON(t, filepath.Join(basePath, "config", "app.json"), appConfig)
	writeMultiHTTPTestJSON(t, filepath.Join(basePath, "config", "database.json"), map[string]interface{}{
		"default": "default",
		"connections": map[string]interface{}{
			"default": map[string]interface{}{
				"type": "mysql", "hostname": "127.0.0.1", "hostport": "3306",
				"database": "test", "username": "test", "password": "",
			},
		},
	})
	writeMultiHTTPTestJSON(t, filepath.Join(basePath, "config", "cache.json"), map[string]interface{}{
		"default": "file",
		"stores":  map[string]interface{}{"file": map[string]interface{}{"type": "file", "path": "runtime/cache"}},
	})
	writeMultiHTTPTestJSON(t, filepath.Join(basePath, "config", "cookie.json"), map[string]interface{}{"path": "/", "secure": false, "httponly": true, "samesite": "Lax"})
	writeMultiHTTPTestJSON(t, filepath.Join(basePath, "config", "csrf.json"), map[string]interface{}{"safe_methods": []string{"GET", "HEAD", "OPTIONS"}})
	writeMultiHTTPTestJSON(t, filepath.Join(basePath, "config", "lang.json"), map[string]interface{}{"default_lang": "zh-cn"})
	writeMultiHTTPTestJSON(t, filepath.Join(basePath, "config", "log.json"), map[string]interface{}{
		"default":  "file",
		"channels": map[string]interface{}{"file": map[string]interface{}{"type": "file", "path": "runtime/log"}},
	})
	writeMultiHTTPTestJSON(t, filepath.Join(basePath, "config", "route.json"), map[string]interface{}{"url_route_must": true})
	writeMultiHTTPTestJSON(t, filepath.Join(basePath, "config", "session.json"), map[string]interface{}{"name": "TESTSESSID", "type": "file", "storage_path": "runtime/session", "cookie_path": "/", "expire": 1440})
	writeMultiHTTPTestJSON(t, filepath.Join(basePath, "config", "view.json"), map[string]interface{}{"view_path": "app/view", "view_suffix": "html", "cache": false})

	manager, err := framework.NewApplicationManagerFromDefinitions(basePath, definitions, true)
	if err != nil {
		t.Fatalf("创建测试应用管理器失败: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func multiHTTPTestApplicationRegister() func(*framework.App) error {
	return func(app *framework.App) error {
		return app.RegisterRouteLoader(func(app *framework.App) error {
			router, err := framework.ResolveServiceAs[*route.Router](app, framework.ServiceRoute)
			if err != nil {
				return err
			}
			_, err = router.Get("/health", func(request *fwcontext.Request) *fwcontext.Response {
				application, _ := request.ApplicationContext()
				return fwcontext.NewResponse().Content(application.Name() + "|healthy")
			})
			if err != nil {
				return err
			}
			_, err = router.Any("/users", func(request *fwcontext.Request) *fwcontext.Response {
				application, _ := request.ApplicationContext()
				return fwcontext.NewResponse().Content(application.Name() + "|" + request.Path() + "|")
			})
			if err != nil {
				return err
			}
			_, err = router.Any("/echo", func(request *fwcontext.Request) *fwcontext.Response {
				application, _ := request.ApplicationContext()
				content, _ := request.Body()
				return fwcontext.NewResponse().Content(application.Name() + "|" + request.Path() + "|" + string(content) + "|" + strconv.FormatBool(request.IsSsl()))
			})
			return err
		})
	}
}

func writeMultiHTTPTestJSON(t testing.TB, path string, value interface{}) {
	t.Helper()
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("编码测试配置失败: %v", err)
	}
	if err = os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
}
