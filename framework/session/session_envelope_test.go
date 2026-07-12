package session

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"thinkgo/framework/cookie"
)

// TestDecodeSessionEnvelopeRejectsEveryInvalidState 验证版本、字段、墓碑和 JSON 边界都严格收敛。
func TestDecodeSessionEnvelopeRejectsEveryInvalidState(t *testing.T) {
	invalid := []string{
		"",
		`{"version":2,"data":{}}`,
		`{"version":1,"expire_at":-1,"data":{}}`,
		`{"version":1,"unknown":true,"data":{}}`,
		`{"version":1,"revoked":true,"expire_at":100,"data":{"uid":1}}`,
		`{"version":1,"revoked":true,"data":null}`,
		`{"version":1,"data":null}`,
		`{"version":1,"data":{"bad\nkey":1}}`,
		`{"version":1,"data":{}} {"extra":true}`,
	}
	for _, content := range invalid {
		if _, err := decodeSessionEnvelope(content, 1024); !errors.Is(err, ErrCorruptSession) {
			t.Fatalf("非法信封 %q 应返回 ErrCorruptSession，实际为 %v", content, err)
		}
	}
	if _, err := decodeSessionEnvelope(strings.Repeat("x", 257), 256); !errors.Is(err, ErrCorruptSession) ||
		!errors.Is(err, ErrSessionDataTooLarge) {
		t.Fatalf("超大信封应同时标记损坏与超限，实际为 %v", err)
	}
}

// TestMissingPersistedRecordRotatesIDBeforeSaving 验证后端记录消失时不会原 ID 重建。
func TestMissingPersistedRecordRotatesIDBeforeSaving(t *testing.T) {
	driver := newCountingDriver()
	manager := newTestSessionManager(t, driver, map[string]interface{}{"name": "SID"}, nil)
	cookieValue, oldID := persistedSessionFixture(t, manager)
	recorder := httptest.NewRecorder()
	requestSession, err := manager.NewRequestSession(requestFromSessionCookie(cookieValue), recorder)
	if err != nil {
		t.Fatalf("初始化已持久化 Session 失败: %v", err)
	}
	if err = driver.Delete(oldID); err != nil {
		t.Fatalf("模拟后端记录消失失败: %v", err)
	}
	if err = requestSession.Set("after_missing", true); err != nil {
		t.Fatalf("设置待保存数据失败: %v", err)
	}
	if err = requestSession.Save(); err != nil {
		t.Fatalf("记录消失后轮换保存失败: %v", err)
	}
	if requestSession.id == oldID {
		t.Fatal("消失的已持久化 ID 不得被原地重建")
	}
	if _, found := driver.snapshot(oldID); found {
		t.Fatal("旧 ID 不应在轮换保存后重新出现")
	}
	if _, found := driver.snapshot(requestSession.id); !found {
		t.Fatal("轮换后的新 ID 应成功持久化")
	}
}

// TestSessionConstructorsRejectNilDependencies 验证普通 nil 与类型化 nil 均在构造期失败。
func TestSessionConstructorsRejectNilDependencies(t *testing.T) {
	cookieFactory, err := cookie.NewCookie(nil)
	if err != nil {
		t.Fatalf("创建 Cookie 工厂失败: %v", err)
	}
	var typedNil *countingDriver
	if _, err = NewSession(nil, typedNil, cookieFactory); !errors.Is(err, ErrInvalidSessionDependency) {
		t.Fatalf("类型化 nil 驱动应被拒绝，实际为 %v", err)
	}
	if _, err = NewSession(nil, newCountingDriver(), nil); !errors.Is(err, ErrInvalidSessionDependency) {
		t.Fatalf("nil Cookie 工厂应被拒绝，实际为 %v", err)
	}
	manager := newTestSessionManager(t, newCountingDriver(), map[string]interface{}{"name": "SID"}, nil)
	if manager.GetConfig().Name != "SID" || (*Session)(nil).GetConfig() != (Config{}) {
		t.Fatal("GetConfig 应返回配置副本且 nil 接收者返回零值")
	}
	if _, err = manager.NewRequestSession(nil, nil); !errors.Is(err, ErrInvalidSessionDependency) {
		t.Fatalf("nil 请求应被拒绝，实际为 %v", err)
	}
}

// TestSessionTracksEncodedDataSizeExactly 验证增量计数覆盖转义键、替换、删除和清空，避免每次 Set 全量编码。
func TestSessionTracksEncodedDataSizeExactly(t *testing.T) {
	manager := newTestSessionManager(t, newCountingDriver(), map[string]interface{}{"name": "SID"}, nil)
	requestSession, err := manager.NewRequestSession(
		httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder(),
	)
	if err != nil {
		t.Fatalf("初始化大小计数 Session 失败: %v", err)
	}
	assertSize := func() {
		t.Helper()
		encoded, marshalErr := json.Marshal(requestSession.data)
		if marshalErr != nil {
			t.Fatalf("编码 Session 数据失败: %v", marshalErr)
		}
		if requestSession.dataJSONBytes != len(encoded) {
			t.Fatalf("增量 JSON 大小错误: tracked=%d actual=%d data=%s", requestSession.dataJSONBytes, len(encoded), encoded)
		}
		envelope := sessionEnvelope{Version: sessionEnvelopeVersion, Data: requestSession.data}
		if requestSession.config.Expire > 0 {
			envelope.ExpireAt = requestSession.nowTime().Add(
				time.Duration(requestSession.config.Expire) * time.Second,
			).Unix()
		}
		encodedEnvelope, marshalErr := json.Marshal(envelope)
		if marshalErr != nil {
			t.Fatalf("编码 Session 信封失败: %v", marshalErr)
		}
		trackedEnvelope := activeSessionEnvelopeBytes(
			requestSession.dataJSONBytes, requestSession.config, requestSession.nowTime(),
		)
		if trackedEnvelope != len(encodedEnvelope) {
			t.Fatalf("增量信封大小错误: tracked=%d actual=%d envelope=%s", trackedEnvelope, len(encodedEnvelope), encodedEnvelope)
		}
	}
	assertSize()
	if err = requestSession.Set(`quoted"key`, "first"); err != nil {
		t.Fatalf("设置转义键失败: %v", err)
	}
	assertSize()
	if err = requestSession.Set(`quoted"key`, map[string]interface{}{"nested": true}); err != nil {
		t.Fatalf("覆盖转义键失败: %v", err)
	}
	assertSize()
	if err = requestSession.Set("other", 2); err != nil {
		t.Fatalf("设置第二个键失败: %v", err)
	}
	assertSize()
	if err = requestSession.Set("html<>&\u2028", true); err != nil {
		t.Fatalf("设置 HTML 转义键失败: %v", err)
	}
	assertSize()
	if err = requestSession.Delete(`quoted"key`); err != nil {
		t.Fatalf("删除转义键失败: %v", err)
	}
	assertSize()
	if err = requestSession.Clear(); err != nil {
		t.Fatalf("清空 Session 失败: %v", err)
	}
	assertSize()
}
