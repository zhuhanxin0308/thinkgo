package session

import (
	"errors"
	"net/http/httptest"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

// TestRotateSessionReleasesReadLockDuringDriverIO 验证轮换在慢速驱动 I/O 期间不持有 s.mu，
// 并发读取零阻塞，并发写入在锁外等待并在轮换提交或回滚后正确执行。
func TestRotateSessionReleasesReadLockDuringDriverIO(t *testing.T) {
	for _, op := range []string{"regenerate", "authenticate", "rollback"} {
		t.Run(op, func(t *testing.T) {
			gate := &authenticationUpdateGate{
				Driver:  authenticationBackend(t, "memory"),
				entered: make(chan struct{}),
				release: make(chan struct{}),
			}
			failure := errors.New("driver io failure")
			baseDriver := Driver(gate)
			rejectDriver := &authenticationRejectCommitDriver{Driver: gate, failure: failure}
			if op == "rollback" {
				baseDriver = rejectDriver
			}
			manager := newTestSessionManager(t, baseDriver, map[string]interface{}{"expire": 600}, nil)
			t.Cleanup(func() { _ = manager.Close() })

			oldCookie, oldID := seedAuthenticationSizeSession(t, manager, 10)
			if op == "rollback" {
				rejectDriver.reject.Store(true)
			}
			writer := httptest.NewRecorder()
			current, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), writer)
			if err != nil {
				t.Fatal(err)
			}

			gate.armed.Store(true)
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate.release) }) }
			defer release()

			rotateDone := make(chan error, 1)
			go func() {
				if op == "authenticate" {
					rotateDone <- current.SetAuthenticated("uid", "user-42")
				} else {
					rotateDone <- current.Regenerate()
				}
			}()
			<-gate.entered

			// 1. 驱动 I/O 阻塞期间，并发只读操作不应阻塞。
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				if v, ok := current.Get("captcha"); !ok || v != "captcha-before-login" {
					t.Errorf("轮换期间并发 Get 结果异常: %v %t", v, ok)
				}
				if !current.Has("csrf") {
					t.Error("轮换期间并发 Has 结果异常")
				}
				if all := current.All(); all["csrf"] != "csrf-before-login" {
					t.Errorf("轮换期间并发 All 结果异常: %#v", all)
				}
			}()
			select {
			case <-readDone:
			case <-time.After(time.Second):
				t.Fatal("rotateSessionLocked 在驱动 I/O 期间持锁阻塞了并发读取")
			}

			// 2. 启动一个并发写入（在轮换屏障外等待），再次验证读取仍不被写锁饥饿阻塞。
			setStarted := make(chan struct{})
			setDone := make(chan error, 1)
			go func() {
				close(setStarted)
				setDone <- current.Set("after_wait", "ok")
			}()
			<-setStarted
			runtime.Gosched()

			readWhileSetWaiting := make(chan struct{})
			go func() {
				defer close(readWhileSetWaiting)
				_ = current.Has("captcha")
				_, _ = current.Get("csrf")
			}()
			select {
			case <-readWhileSetWaiting:
			case <-time.After(time.Second):
				t.Fatal("等待轮换完成的并发写入阻塞了只读访问")
			}

			release()
			rotErr := <-rotateDone
			setErr := <-setDone
			if setErr != nil {
				t.Fatalf("轮换结束后等待中的 Set 失败: %v", setErr)
			}
			if op == "rollback" {
				if !errors.Is(rotErr, failure) {
					t.Fatalf("期望返回驱动失败 %v，实际为 %v", failure, rotErr)
				}
				if current.id != oldID {
					t.Fatalf("失败轮换不应改变 Session ID: got=%s want=%s", current.id, oldID)
				}
			} else {
				if rotErr != nil {
					t.Fatalf("轮换失败: %v", rotErr)
				}
				if current.id == oldID {
					t.Fatal("成功轮换应更新 Session ID")
				}
				if op == "authenticate" {
					if uid, ok := current.Get("uid"); !ok || uid != "user-42" {
						t.Fatalf("认证身份丢失: %v %t", uid, ok)
					}
				}
			}
			if v, ok := current.Get("after_wait"); !ok || v != "ok" {
				t.Fatalf("等待轮换后的并发写入未生效: %v %t", v, ok)
			}
		})
	}
}

// TestRotateSessionAbnormalDriverExitReleasesLocks 验证驱动 panic/Goexit 不会永久锁死 Session 或挂起等待协程。
func TestRotateSessionAbnormalDriverExitReleasesLocks(t *testing.T) {
	for _, mode := range []string{"panic", "goexit"} {
		t.Run(mode, func(t *testing.T) {
			manager, backend, cookie := newPersistedStringSession(t)
			request := loadStringSession(t, manager, cookie)
			marker := new(int)
			request.driver = &terminatingSessionDriver{Driver: backend, terminate: func() {
				if mode == "panic" {
					panic(marker)
				}
				runtime.Goexit()
			}}
			var recovered interface{}
			exited := make(chan struct{})
			go func() {
				defer close(exited)
				defer func() { recovered = recover() }()
				_ = request.Regenerate()
			}()
			<-exited
			if mode == "panic" && recovered != marker {
				t.Fatalf("panic 身份改变: %v", recovered)
			}
			if !request.mu.TryLock() {
				t.Fatal("异常退出后 s.mu 未释放")
			}
			rotating := request.rotating
			request.mu.Unlock()
			if rotating != nil {
				t.Fatal("异常退出后 s.rotating 未清理")
			}
			if !request.saveMu.TryLock() {
				t.Fatal("异常退出后 s.saveMu 未释放")
			}
			request.saveMu.Unlock()
		})
	}
}

// TestNewRequestSessionRejectsActiveRedisClear 验证 Redis 清理租约活动期间 HTTP bootstrap 拒绝加载旧会话。
func TestNewRequestSessionRejectsActiveRedisClear(t *testing.T) {
	server := miniredis.RunT(t)
	port, err := strconv.Atoi(server.Port())
	if err != nil {
		t.Fatal(err)
	}
	redisDriver, err := driver.NewRedis(map[string]interface{}{
		"host":       server.Host(),
		"port":       port,
		"prefix":     "thinkgo:session:clear-bootstrap:",
		"timeout_ms": 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = redisDriver.Close() })

	manager := newTestSessionManager(t, redisDriver, map[string]interface{}{
		"name":              "SID",
		"expire":            600,
		"reject_unknown_id": true,
	}, nil)
	oldCookie, oldID := persistedSessionFixture(t, manager)

	// 模拟 Clear 已写入活动清理代际与有效租约、但尚未扫描删除 data:<oldID> 的窗口。
	const prefix = "thinkgo:session:clear-bootstrap:"
	if err := server.Set(prefix+"meta:generation", "clearing:active-owner"); err != nil {
		t.Fatal(err)
	}
	if err := server.Set(prefix+"meta:clearing", "clearing:active-owner"); err != nil {
		t.Fatal(err)
	}
	server.SetTTL(prefix+"meta:clearing", 5*time.Second)

	if !server.Exists(prefix + "data:" + oldID) {
		t.Fatal("测试前置条件失败：旧会话键应尚未被扫描删除")
	}
	if _, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), httptest.NewRecorder()); !errors.Is(err, driver.ErrSessionCleared) {
		t.Fatalf("清理活动期间 HTTP bootstrap 应返回 ErrSessionCleared，实际为 %v", err)
	}

	// 正常完成 Clear 后，旧 Cookie 不得恢复原数据，新会话正常读写。
	if err := redisDriver.Clear(); err != nil {
		t.Fatal(err)
	}
	resumed, err := manager.NewRequestSession(requestFromSessionCookie(oldCookie), httptest.NewRecorder())
	if err != nil {
		t.Fatalf("清理完成后新请求初始化失败: %v", err)
	}
	if resumed.id == oldID || resumed.Has("uid") {
		t.Fatalf("清理完成后仍恢复了旧会话: id=%s hasUID=%t", resumed.id, resumed.Has("uid"))
	}
	if err := resumed.Set("uid", 200); err != nil {
		t.Fatal(err)
	}
	if err := resumed.Save(); err != nil {
		t.Fatal(err)
	}
	if got, found, err := redisDriver.Read(resumed.id); err != nil || !found || got == "" {
		t.Fatalf("清理完成后后继代际无法正常读取: found=%t err=%v", found, err)
	}
}
