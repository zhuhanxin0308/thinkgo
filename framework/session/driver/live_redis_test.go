//go:build integration

package driver

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var liveRedisSessionSequence atomic.Uint64

type liveRedisSessionEnvelope struct {
	Version  int                        `json:"version"`
	ExpireAt int64                      `json:"expire_at"`
	Data     map[string]json.RawMessage `json:"data"`
}

// TestLiveRedisSessionDriver 验证真实 Redis Session 的 TTL、原子更新和命名空间清理。
func TestLiveRedisSessionDriver(t *testing.T) {
	config, configured := liveRedisSessionConfig(t)
	if !configured {
		t.Skip("THINKGO_LIVE_REDIS_HOST/PORT 未配置")
	}
	config["prefix"] = fmt.Sprintf("thinkgo:live:session:%d:%d:", os.Getpid(), liveRedisSessionSequence.Add(1))
	driver, err := NewRedis(config)
	if err != nil {
		t.Fatalf("创建真实 Redis Session 驱动失败: %v", err)
	}
	t.Cleanup(func() {
		if clearErr := driver.Clear(); clearErr != nil {
			t.Errorf("清理真实 Redis Session 命名空间失败: %v", clearErr)
		}
		if closeErr := driver.Close(); closeErr != nil {
			t.Errorf("关闭真实 Redis Session 驱动失败: %v", closeErr)
		}
	})
	if err = driver.client.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("真实 Redis Session Ping 失败: %v", err)
	}

	const (
		workers          = 8
		updatesPerWorker = 25
	)
	initial := liveRedisSessionEnvelope{Version: 1, ExpireAt: time.Now().Add(2 * time.Minute).Unix(), Data: map[string]json.RawMessage{}}
	initial.Data["counter"], _ = json.Marshal(0)
	initialBytes, _ := json.Marshal(initial)
	const sessionID = "live-session"
	if err = driver.Write(sessionID, string(initialBytes)); err != nil {
		t.Fatalf("真实 Redis Session 初始写入失败: %v", err)
	}

	var waitGroup sync.WaitGroup
	var firstErr error
	var errorOnce sync.Once
	for worker := 0; worker < workers; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for index := 0; index < updatesPerWorker; index++ {
				err := driver.Update(sessionID, func(data string, found bool) (string, bool, error) {
					if !found {
						return "", false, fmt.Errorf("并发更新时 Session 意外缺失")
					}
					var envelope liveRedisSessionEnvelope
					if decodeErr := json.Unmarshal([]byte(data), &envelope); decodeErr != nil {
						return "", false, decodeErr
					}
					var counter int
					if decodeErr := json.Unmarshal(envelope.Data["counter"], &counter); decodeErr != nil {
						return "", false, decodeErr
					}
					counter++
					envelope.Data["counter"], _ = json.Marshal(counter)
					next, encodeErr := json.Marshal(envelope)
					return string(next), false, encodeErr
				})
				if err != nil {
					errorOnce.Do(func() { firstErr = err })
					return
				}
			}
		}()
	}
	waitGroup.Wait()
	if firstErr != nil {
		t.Fatalf("真实 Redis Session 并发更新失败: %v", firstErr)
	}

	data, found, err := driver.Read(sessionID)
	if err != nil || !found {
		t.Fatalf("真实 Redis Session 读取失败: found=%t err=%v", found, err)
	}
	var result liveRedisSessionEnvelope
	if err = json.Unmarshal([]byte(data), &result); err != nil {
		t.Fatalf("真实 Redis Session 信封损坏: %v", err)
	}
	var counter int
	if err = json.Unmarshal(result.Data["counter"], &counter); err != nil || counter != workers*updatesPerWorker {
		t.Fatalf("真实 Redis Session 原子更新计数错误: counter=%d err=%v", counter, err)
	}
	ctx, cancel := driver.context()
	ttl, err := driver.client.PTTL(ctx, driver.dataKey(sessionID)).Result()
	cancel()
	if err != nil || ttl <= 0 {
		t.Fatalf("真实 Redis Session TTL 未写入: ttl=%v err=%v", ttl, err)
	}
}

func liveRedisSessionConfig(t *testing.T) (map[string]interface{}, bool) {
	t.Helper()
	host, hostOK := os.LookupEnv("THINKGO_LIVE_REDIS_HOST")
	portText, portOK := os.LookupEnv("THINKGO_LIVE_REDIS_PORT")
	if !hostOK || !portOK || host == "" || portText == "" {
		return nil, false
	}
	port, err := strconv.ParseInt(portText, 10, 64)
	if err != nil {
		t.Fatalf("真实 Redis 端口配置非法: %v", err)
	}
	config := map[string]interface{}{"host": host, "port": port, "timeout_ms": 1000}
	if password, exists := os.LookupEnv("THINKGO_LIVE_REDIS_PASSWORD"); exists {
		config["password"] = password
	}
	if database, exists := os.LookupEnv("THINKGO_LIVE_REDIS_DB"); exists && database != "" {
		value, parseErr := strconv.ParseInt(database, 10, 64)
		if parseErr != nil {
			t.Fatalf("真实 Redis 数据库配置非法: %v", parseErr)
		}
		config["select"] = value
	}
	return config, true
}
