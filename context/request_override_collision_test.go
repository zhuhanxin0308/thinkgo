package context

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// TestRequestOverrideSpellingPrecedence 验证规范拼写和没有规范拼写时的字典序。
func TestRequestOverrideSpellingPrecedence(t *testing.T) {
	request := newRequestForTest(t, httptest.NewRequest(http.MethodGet, "/", nil))
	headers := map[string]string{
		"Authorization": "canonical", "AUTHORIZATION": "upper", "authorization": "lower",
		"X-TENANT": "first", "x-tenant": "last", "X-Single": "single",
	}
	server := map[string]any{
		"HTTP_HOST": "canonical", " http_host ": "padded", "http_host": "lower",
		" Custom ": "first", "custom": "last",
	}
	for range 64 {
		if request.WithHeader(headers).WithServer(server) != request {
			t.Fatal("兼容入口应支持链式调用")
		}
		if request.Header("authorization") != "canonical" || request.Header("X-Tenant") != "first" || request.Header("x-single") != "single" {
			t.Fatal("Header 覆盖未按拼写优先级选择")
		}
		if request.Server(" http_host ") != "canonical" || request.Server("CUSTOM") != "first" {
			t.Fatal("SERVER 覆盖未按拼写优先级选择")
		}
	}
}

// TestStrictRequestOverridePreservesOriginalSources 验证冲突整批拒绝、原始多值头与 SERVER 回退不受影响。
func TestStrictRequestOverridePreservesOriginalSources(t *testing.T) {
	raw := httptest.NewRequest(http.MethodGet, "https://tenant.test/", nil)
	raw.Header["X-Tag"] = []string{"first", "second"}
	request := newRequestForTest(t, raw)
	for _, other := range []string{"same", "different"} {
		err := request.WithHeaderStrict(map[string]string{"X-Tag": "same", "x-tag": other, "X-New": "partial"})
		if !errors.Is(err, ErrRequestOverrideConflict) || strings.Contains(err.Error(), other) {
			t.Fatalf("严格 Header 应拒绝重复名称且诊断不得包含值: %v", err)
		}
		err = request.WithServerStrict(map[string]any{"HTTP_HOST": "same", " http_host ": other, "NEW": "partial"})
		if !errors.Is(err, ErrRequestOverrideConflict) {
			t.Fatalf("严格 SERVER 应拒绝重复名称: %v", err)
		}
	}
	if request.Header("X-Tag") != "first, second" || request.Server("HTTP_HOST") != "tenant.test" ||
		request.Header("X-New") != "" || request.Server("NEW") != "" || request.OverrideError() != nil {
		t.Fatal("严格覆盖失败改变了原生请求读取或已发布诊断")
	}
	sources, err := request.Sources()
	if err != nil || !slices.Equal(sources.Header.Values("X-Tag"), []string{"first", "second"}) {
		t.Fatalf("原始多值头快照丢失: %v %v", sources.Header, err)
	}
	if err := request.WithHeaderStrict(map[string]string{"X-Tag": "replacement"}); err != nil {
		t.Fatal(err)
	}
	if err := request.WithHeaderStrict(map[string]string{"X-Tag": "bad", "x-tag": "bad"}); !errors.Is(err, ErrRequestOverrideConflict) {
		t.Fatalf("已有快照时仍必须拒绝冲突: %v", err)
	}
	sources, err = request.Sources()
	if err != nil {
		t.Fatal(err)
	}
	sources.Header.Set("X-Tag", "external")
	if request.Header("X-Tag") != "replacement" || !slices.Equal(raw.Header.Values("X-Tag"), []string{"first", "second"}) {
		t.Fatal("快照修改污染了已发布的覆盖或原始请求")
	}
}

// TestRequestOverrideDiagnosticsTrackPublishedSources 验证诊断按来源更新，接收重复同值也保留歧义提示。
func TestRequestOverrideDiagnosticsTrackPublishedSources(t *testing.T) {
	request := newRequestForTest(t, httptest.NewRequest(http.MethodGet, "/", nil))
	request.WithHeader(map[string]string{"X-Key": "private-header", "x-key": "private-header"}).
		WithServer(map[string]any{"KEY": []int{1}, "key": []int{1}})
	err := request.OverrideError()
	if !errors.Is(err, ErrRequestOverrideConflict) || !strings.Contains(err.Error(), "X-Key") || !strings.Contains(err.Error(), "KEY") || strings.Contains(err.Error(), "private-header") {
		t.Fatalf("诊断应能定位两个来源的冲突且不披露值: %v", err)
	}
	request.WithServer(nil)
	if err := request.OverrideError(); !errors.Is(err, ErrRequestOverrideConflict) || strings.Contains(err.Error(), "KEY") {
		t.Fatalf("成功替换 SERVER 后应仅保留 Header 诊断: %v", err)
	}
	if err := request.WithHeaderStrict(nil); err != nil || request.OverrideError() != nil || request.Header("X-Key") != "" {
		t.Fatalf("显式空覆盖应清除该来源的数据和诊断: %v %v", err, request.OverrideError())
	}
	var absent *Request
	if absent.WithHeader(nil) != nil || absent.WithServer(nil) != nil || absent.OverrideError() != nil ||
		absent.WithHeaderStrict(nil) == nil || absent.WithServerStrict(nil) == nil {
		t.Fatal("nil 接收者的兼容及严格入口行为异常")
	}
}

// TestStrictServerOverridePreservesDerivedCacheAndOwnership 验证严格覆盖与派生缓存、可变输入的所有权边界。
func TestStrictServerOverridePreservesDerivedCacheAndOwnership(t *testing.T) {
	request := newRequestForTest(t, httptest.NewRequest(http.MethodGet, "/", nil))
	values := []string{"original"}
	if err := request.WithServerStrict(map[string]any{"script_filename": "/app/index.php", "script_name": "/index.php", "typed": values}); err != nil {
		t.Fatal(err)
	}
	values[0] = "modified"
	if request.BaseFile() != "/index.php" {
		t.Fatalf("未读取已发布的入口脚本: %q", request.BaseFile())
	}
	err := request.WithServerStrict(map[string]any{"SCRIPT_NAME": "/new", " script_name ": "/bad", "SCRIPT_FILENAME": "/app/new"})
	if !errors.Is(err, ErrRequestOverrideConflict) || request.BaseFile() != "/index.php" || request.Server("SCRIPT_NAME") != "/index.php" {
		t.Fatalf("失败批次改变了入口脚本的缓存或数据: %v", err)
	}
	read, ok := request.serverOverride("TYPED")
	if !ok || read.([]string)[0] != "original" {
		t.Fatalf("外部输入修改污染了服务器集合: %v", read)
	}
	read.([]string)[0] = "outside"
	again, _ := request.serverOverride("TYPED")
	if again.([]string)[0] != "original" {
		t.Fatal("读取快照与 SERVER 内部数据共享存储")
	}
	if err := request.WithServerStrict(map[string]any{"SCRIPT_FILENAME": "/app/new.php", "SCRIPT_NAME": "/new.php"}); err != nil || request.BaseFile() != "/new.php" {
		t.Fatalf("成功批次未刷新入口脚本: %v %q", err, request.BaseFile())
	}
}
