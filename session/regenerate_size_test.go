package session

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRegenerateRejectsMergedOverflowBeforeRevocation 覆盖并发合并及实际 JSON 转义导致的超限。
func TestRegenerateRejectsMergedOverflowBeforeRevocation(t *testing.T) {
	const byteLimit = 512
	for _, kind := range []string{"memory", "file"} {
		for _, scenario := range []string{"并发合并", "JSON转义"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				backend := authenticationBackend(t, kind)
				manager := newTestSessionManager(t, backend, map[string]interface{}{"max_data_bytes": byteLimit, "expire": 0, "reject_unknown_id": true}, nil)
				t.Cleanup(func() { _ = manager.Close() })
				cookie, oldID := seedAuthenticationSizeSession(t, manager, 0)
				if scenario == "JSON转义" {
					// 驱动允许合法但尚未进行 HTML 转义的原始信封。
					original := `{"version":1,"data":{"padding":"` + strings.Repeat("<", 85) + `"}}`
					if err := backend.Write(oldID, original); err != nil {
						t.Fatal(err)
					}
				}
				writer := httptest.NewRecorder()
				current, err := manager.NewRequestSession(requestFromSessionCookie(cookie), writer)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "并发合并" {
					other, err := manager.NewRequestSession(requestFromSessionCookie(cookie), httptest.NewRecorder())
					if err != nil {
						t.Fatal(err)
					}
					if err := current.Set("left", strings.Repeat("l", 220)); err != nil {
						t.Fatal(err)
					}
					if err := other.Set("right", strings.Repeat("r", 220)); err != nil {
						t.Fatal(err)
					}
					if err := other.Save(); err != nil {
						t.Fatal(err)
					}
				}
				storedBefore, found, err := backend.Read(oldID)
				if err != nil || !found {
					t.Fatal("缺少旧会话基线", err)
				}
				beforeState, beforeData := current.snapshotForSave(), current.All()
				if err := current.Regenerate(); !errors.Is(err, ErrSessionDataTooLarge) {
					t.Fatalf("应在撤销前拒绝实际信封超限: %v", err)
				}
				storedAfter, found, err := backend.Read(oldID)
				if err != nil || !found || storedAfter != storedBefore || !reflect.DeepEqual(beforeState, current.snapshotForSave()) || !reflect.DeepEqual(beforeData, current.All()) || len(writer.Header().Values("Set-Cookie")) != 0 {
					t.Fatalf("失败轮换改变旧记录、请求状态或 Cookie: %v", err)
				}
				resumed, err := manager.NewRequestSession(requestFromSessionCookie(cookie), httptest.NewRecorder())
				if err != nil || resumed.id != oldID {
					t.Fatal("旧 Cookie 无法恢复", err)
				}
				if err := current.Clear(); err != nil {
					t.Fatal(err)
				}
				if err := current.Set("retry", "成功"); err != nil {
					t.Fatal(err)
				}
				if err := current.Regenerate(); err != nil || current.id == oldID {
					t.Fatal("删减后无法重新轮换", err)
				}
				if err := current.Save(); err != nil {
					t.Fatal("新 ID 无法保存", err)
				}
				if err := resumed.Set("stale", true); err != nil {
					t.Fatal(err)
				}
				if err := resumed.Save(); !errors.Is(err, ErrSessionRevoked) {
					t.Fatalf("成功轮换后旧请求不应复活旧 ID: %v", err)
				}
			})
		}
	}
}

// TestRegenerateSerializesCanonicalBudget 确保等待轮换的 Set 依据真实编码后的新状态判断大小。
func TestRegenerateSerializesCanonicalBudget(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			const byteLimit = 512
			gate := &authenticationUpdateGate{Driver: authenticationBackend(t, kind), entered: make(chan struct{}), release: make(chan struct{})}
			manager := newTestSessionManager(t, gate, map[string]interface{}{"max_data_bytes": byteLimit, "expire": 0}, nil)
			t.Cleanup(func() { _ = manager.Close() })
			cookie, oldID := seedAuthenticationSizeSession(t, manager, 0)
			original := `{"version":1,"data":{"padding":"` + strings.Repeat("<", 60) + `"}}`
			if err := gate.Driver.Write(oldID, original); err != nil {
				t.Fatal(err)
			}
			current, err := manager.NewRequestSession(requestFromSessionCookie(cookie), httptest.NewRecorder())
			if err != nil {
				t.Fatal(err)
			}
			gate.armed.Store(true)
			rotated, modified := make(chan error, 1), make(chan error, 1)
			var once sync.Once
			release := func() { once.Do(func() { close(gate.release) }) }
			defer release()
			go func() { rotated <- current.Regenerate() }()
			<-gate.entered
			go func() { modified <- current.Set("extra", strings.Repeat("x", 200)) }()
			release()
			rotationErr, setErr := <-rotated, <-modified
			if rotationErr != nil || !errors.Is(setErr, ErrSessionDataTooLarge) || current.Has("extra") {
				t.Fatalf("并发写入推翻轮换预算: 轮换=%v, Set=%v", rotationErr, setErr)
			}
			if err := current.Save(); err != nil {
				t.Fatal(err)
			}
			stored, found, err := gate.Driver.Read(current.id)
			if err != nil || !found || len(stored) > byteLimit {
				t.Fatal("轮换后的真实信封不可保存", err)
			}
		})
	}
}

// TestRegenerateRechecksExpiryAfterDriverWait 验证驱动等待期间过期时，不撤销或发布新身份。
func TestRegenerateRechecksExpiryAfterDriverWait(t *testing.T) {
	gate := &authenticationUpdateGate{Driver: authenticationBackend(t, "memory"), entered: make(chan struct{}), release: make(chan struct{})}
	manager := newTestSessionManager(t, gate, map[string]interface{}{"expire": 1}, nil)
	t.Cleanup(func() { _ = manager.Close() })
	var clock atomic.Int64
	clock.Store(time.Now().Unix())
	manager.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	cookie, oldID := seedAuthenticationSizeSession(t, manager, 0)
	current, err := manager.NewRequestSession(requestFromSessionCookie(cookie), httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := gate.Driver.Read(oldID)
	if err != nil {
		t.Fatal(err)
	}
	gate.armed.Store(true)
	done := make(chan error, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	defer release()
	go func() { done <- current.Regenerate() }()
	<-gate.entered
	clock.Add(2)
	release()
	if err := <-done; !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("等待后未拒绝过期会话: %v", err)
	}
	after, found, err := gate.Driver.Read(oldID)
	if err != nil || !found || before != after || current.id != oldID {
		t.Fatal("过期轮换改变旧记录或请求 ID", err)
	}
}

// TestRegenerateExactLimit 验证完整信封恰好达到上限时，新旧两种持久化状态均可轮换并保存。
func TestRegenerateExactLimit(t *testing.T) {
	const byteLimit = 512
	for _, kind := range []string{"memory", "file"} {
		for _, persisted := range []bool{false, true} {
			backend := authenticationBackend(t, kind)
			manager := newTestSessionManager(t, backend, map[string]interface{}{"max_data_bytes": byteLimit, "expire": 0}, nil)
			t.Cleanup(func() { _ = manager.Close() })
			current, err := manager.NewRequestSession(httptest.NewRequest("GET", "/", nil), httptest.NewRecorder())
			if err != nil {
				t.Fatal(err)
			}
			empty, err := json.Marshal(sessionEnvelope{Version: sessionEnvelopeVersion, Data: map[string]json.RawMessage{"payload": json.RawMessage(`""`)}})
			if err != nil {
				t.Fatal(err)
			}
			if err := current.Set("payload", strings.Repeat("p", byteLimit-len(empty))); err != nil {
				t.Fatal(err)
			}
			if persisted {
				if err := current.Save(); err != nil {
					t.Fatal(err)
				}
			}
			if err := current.Regenerate(); err != nil {
				t.Fatal(err)
			}
			if err := current.Save(); err != nil {
				t.Fatal(err)
			}
			stored, found, err := backend.Read(current.id)
			if err != nil || !found || len(stored) != byteLimit {
				t.Fatalf("未实际覆盖完整信封边界: 长度=%d, 错误=%v", len(stored), err)
			}
		}
	}
}
