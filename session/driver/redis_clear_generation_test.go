package driver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisClearFencesPendingUpdates(t *testing.T) {
	backend, _ := newMiniredisSession(t)
	assertRedisClearFencesPendingUpdates(t, backend)
}

func TestRedisClearPreservesNestedGeneration(t *testing.T) {
	backend, _ := newMiniredisSession(t)
	nested := newRedisClearPeer(t, backend)
	nested.prefix += redisSessionDataPrefix
	if err := nested.Clear(); err != nil {
		t.Fatal(err)
	}
	value := marshalRedisTestEnvelope(1, time.Now().Add(time.Minute))
	if err := nested.Write("nested", value); err != nil {
		t.Fatal(err)
	}
	if err := nested.Update("nested", func(current string, found bool) (string, bool, error) {
		if err := backend.Clear(); err != nil {
			return "", false, err
		}
		return current, false, nil
	}); err != nil {
		t.Fatalf("父命名空间清理破坏嵌套代际: %v", err)
	}
}

func newRedisClearPeer(t *testing.T, backend *Redis) *Redis {
	t.Helper()
	options := backend.client.Options()
	other := &Redis{client: redis.NewClient(&redis.Options{
		Addr: options.Addr, Username: options.Username, Password: options.Password, DB: options.DB,
		TLSConfig: options.TLSConfig, ContextTimeoutEnabled: true,
		DialTimeout: backend.timeout, ReadTimeout: backend.timeout, WriteTimeout: backend.timeout,
	}), prefix: backend.prefix, timeout: backend.timeout}
	t.Cleanup(func() { _ = other.Close() })
	return other
}

// redisClearCommandHook 在实际 Redis 命令之前插入一次确定性交错，不替换被测脚本。
type redisClearCommandHook struct {
	match  func(redis.Cmder) bool
	before func() error
	once   sync.Once
	err    error
}

func (hook *redisClearCommandHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (hook *redisClearCommandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (hook *redisClearCommandHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if hook.match(command) {
			hook.once.Do(func() { hook.err = hook.before() })
			if hook.err != nil {
				return hook.err
			}
		}
		return next(ctx, command)
	}
}

func TestRedisClearFencesDirectWriteAndDelete(t *testing.T) {
	for _, script := range []string{redisSessionWriteLuaScript, redisSessionDeleteLuaScript} {
		backend, _ := newMiniredisSession(t)
		other := newRedisClearPeer(t, backend)
		initial := marshalRedisTestEnvelope(1, time.Now().Add(time.Minute))
		if err := backend.Write("direct", initial); err != nil {
			t.Fatal(err)
		}
		backend.client.AddHook(&redisClearCommandHook{
			match:  func(command redis.Cmder) bool { return command.Name() == "eval" && command.Args()[1] == script },
			before: other.Clear,
		})
		var err error
		if script == redisSessionWriteLuaScript {
			err = backend.Write("direct", initial)
		} else {
			err = backend.Delete("direct")
		}
		if !errors.Is(err, ErrSessionCleared) {
			t.Fatalf("直接写入或删除未遵循同一清理代际: %v", err)
		}
		if _, found, err := backend.Read("direct"); err != nil || found {
			t.Fatalf("旧提交改变清理结果: %v %v", found, err)
		}
	}
}

func TestRedisClearFailureAndOverlappingClear(t *testing.T) {
	for _, scenario := range []string{"active", "failed", "overlap"} {
		t.Run(scenario, func(t *testing.T) {
			backend, _ := newMiniredisSession(t)
			other := newRedisClearPeer(t, backend)
			value := marshalRedisTestEnvelope(1, time.Now().Add(time.Minute))
			injected := errors.New("测试扫描失败")
			backend.client.AddHook(&redisClearCommandHook{
				match: func(command redis.Cmder) bool { return command.Name() == "scan" },
				before: func() error {
					switch scenario {
					case "active":
						called := false
						err := other.Update("during-clear", func(string, bool) (string, bool, error) {
							called = true
							return value, false, nil
						})
						if !errors.Is(err, ErrSessionCleared) || called {
							t.Errorf("清理期间新回调不得执行: called=%v err=%v", called, err)
						}
					case "failed":
						return injected
					case "overlap":
						if err := other.Clear(); err != nil {
							return err
						}
						return other.Write("successor", value)
					}
					return nil
				},
			})
			err := backend.Clear()
			switch scenario {
			case "active":
				if err != nil {
					t.Fatal(err)
				}
			case "failed":
				if !errors.Is(err, injected) {
					t.Fatalf("扫描错误丢失: %v", err)
				}
			case "overlap":
				if !errors.Is(err, ErrSessionCleared) {
					t.Fatalf("旧 Clear 未识别代际接管: %v", err)
				}
				if got, found, err := other.Read("successor"); err != nil || !found || got != value {
					t.Fatalf("旧 Clear 删除了后继数据: %v %v", found, err)
				}
			}
			if err := other.Write("after-clear", value); err != nil {
				t.Fatalf("清理结束后新会话仍被阻塞: %v", err)
			}
		})
	}
}

// assertRedisClearFencesPendingUpdates 同时供模拟与真实 Redis 合同使用。
func assertRedisClearFencesPendingUpdates(t *testing.T, backend *Redis) {
	t.Helper()
	other := newRedisClearPeer(t, backend)
	for _, remove := range []bool{false, true} {
		const id = "pending-clear"
		initial := marshalRedisTestEnvelope(1, time.Now().Add(time.Minute))
		if err := backend.Write(id, initial); err != nil {
			t.Fatal(err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- backend.Update(id, func(current string, found bool) (string, bool, error) {
				if !found || current != initial {
					return "", false, errors.New("阻塞更新未读取到原会话")
				}
				close(entered)
				<-release
				return marshalRedisTestEnvelope(2, time.Now().Add(time.Minute)), remove, nil
			})
		}()
		select {
		case <-entered:
		case <-time.After(redisTestTimeout):
			close(release)
			t.Fatal("未到达更新回调")
		}
		if err := other.Clear(); err != nil {
			close(release)
			t.Fatal(err)
		}
		close(release)
		select {
		case err := <-done:
			if !errors.Is(err, ErrSessionCleared) {
				t.Error("Clear 之前的更新不应提交成功")
			}
		case <-time.After(redisTestTimeout):
			t.Fatal("旧更新未返回")
		}
		if _, found, err := backend.Read(id); err != nil || found {
			t.Fatalf("已清理会话复活: found=%v err=%v", found, err)
		}
		if err := other.Write(id, initial); err != nil {
			t.Fatalf("清理后的新写入失败: %v", err)
		}
		if ttl, err := other.client.TTL(context.Background(), other.dataKey(id)).Result(); err != nil || ttl <= 0 {
			t.Fatalf("清理后新会话 TTL 丢失: %v %v", ttl, err)
		}
		if err := other.Delete(id); err != nil {
			t.Fatal(err)
		}
	}
}
