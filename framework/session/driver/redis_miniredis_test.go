package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

const (
	redisTestTimeout = 2 * time.Second
	redisTestWorkers = 4
	redisTestUpdates = 8
)

type redisTestEnvelope struct {
	ExpireAt int64          `json:"expire_at"`
	Data     map[string]int `json:"data"`
}

func newMiniredisSession(t *testing.T) (*Redis, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	port, err := strconv.Atoi(server.Port())
	if err != nil {
		t.Fatalf("解析 miniredis 端口失败: %v", err)
	}
	driver, err := NewRedis(map[string]interface{}{
		"host":       server.Host(),
		"port":       port,
		"prefix":     fmt.Sprintf("thinkgo:test:%d:", time.Now().UnixNano()),
		"timeout_ms": redisTestTimeout.Milliseconds(),
	})
	if err != nil {
		t.Fatalf("创建 miniredis Session 驱动失败: %v", err)
	}
	t.Cleanup(func() {
		if err := driver.Close(); err != nil {
			t.Errorf("关闭 miniredis Session 驱动失败: %v", err)
		}
	})
	return driver, server
}

func marshalRedisTestEnvelope(counter int, expireAt time.Time) string {
	data, err := json.Marshal(redisTestEnvelope{
		ExpireAt: expireAt.Unix(),
		Data:     map[string]int{"counter": counter},
	})
	if err != nil {
		panic(fmt.Sprintf("编码 Session 测试信封失败: %v", err))
	}
	return string(data)
}

func decodeRedisTestCounterValue(data string) (int, error) {
	var envelope redisTestEnvelope
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		return 0, err
	}
	return envelope.Data["counter"], nil
}

func decodeRedisTestCounter(t *testing.T, data string) int {
	t.Helper()
	counter, err := decodeRedisTestCounterValue(data)
	if err != nil {
		t.Fatalf("解码 Session 测试信封失败: %v", err)
	}
	return counter
}

// TestRedisSessionRoundTripWithMiniredis 验证 Redis Session 的读写、TTL、删除和清理语义。
func TestRedisSessionRoundTripWithMiniredis(t *testing.T) {
	driver, server := newMiniredisSession(t)
	const sessionID = "round-trip"
	initial := marshalRedisTestEnvelope(1, time.Now().Add(time.Minute))
	if err := driver.Write(sessionID, initial); err != nil {
		t.Fatalf("写入 Session 失败: %v", err)
	}
	if ttl := server.TTL(driver.dataKey(sessionID)); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("Session TTL 不在预期范围: %v", ttl)
	}
	data, found, err := driver.Read(sessionID)
	if err != nil || !found || decodeRedisTestCounter(t, data) != 1 {
		t.Fatalf("读取 Session 结果错误: found=%t data=%q err=%v", found, data, err)
	}
	if err := driver.Update(sessionID, func(current string, found bool) (string, bool, error) {
		if !found {
			return "", false, errors.New("更新时 Session 意外缺失")
		}
		counter, decodeErr := decodeRedisTestCounterValue(current)
		if decodeErr != nil {
			return "", false, decodeErr
		}
		return marshalRedisTestEnvelope(counter+1, time.Now().Add(time.Minute)), false, nil
	}); err != nil {
		t.Fatalf("更新 Session 失败: %v", err)
	}
	data, found, err = driver.Read(sessionID)
	if err != nil || !found || decodeRedisTestCounter(t, data) != 2 {
		t.Fatalf("读取更新后的 Session 结果错误: found=%t data=%q err=%v", found, data, err)
	}
	if err := driver.Delete(sessionID); err != nil {
		t.Fatalf("删除 Session 失败: %v", err)
	}
	if _, found, err := driver.Read(sessionID); err != nil || found {
		t.Fatalf("删除后的 Session 仍可读: found=%t err=%v", found, err)
	}
	if count, err := driver.GC(time.Hour); err != nil || count != 0 {
		t.Fatalf("Redis 原生 TTL 驱动的 GC 结果错误: count=%d err=%v", count, err)
	}
}

// TestRedisSessionConcurrentUpdateWithMiniredis 验证分布式锁能串行化并发 Session 更新。
func TestRedisSessionConcurrentUpdateWithMiniredis(t *testing.T) {
	driver, _ := newMiniredisSession(t)
	const sessionID = "concurrent"
	if err := driver.Write(sessionID, marshalRedisTestEnvelope(0, time.Now().Add(time.Minute))); err != nil {
		t.Fatalf("写入并发测试初始 Session 失败: %v", err)
	}
	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, redisTestWorkers)
	for worker := 0; worker < redisTestWorkers; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for update := 0; update < redisTestUpdates; update++ {
				err := driver.Update(sessionID, func(current string, found bool) (string, bool, error) {
					if !found {
						return "", false, errors.New("并发更新时 Session 意外缺失")
					}
					counter, decodeErr := decodeRedisTestCounterValue(current)
					if decodeErr != nil {
						return "", false, decodeErr
					}
					return marshalRedisTestEnvelope(counter+1, time.Now().Add(time.Minute)), false, nil
				})
				if err != nil {
					errorsChannel <- err
					return
				}
			}
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatalf("并发更新 Session 失败: %v", err)
	}
	data, found, err := driver.Read(sessionID)
	if err != nil || !found || decodeRedisTestCounter(t, data) != redisTestWorkers*redisTestUpdates {
		t.Fatalf("并发更新计数错误: found=%t data=%q err=%v", found, data, err)
	}
}

// TestRedisSessionScopedClearWithMiniredis 验证 Clear 只删除当前驱动命名空间的数据。
func TestRedisSessionScopedClearWithMiniredis(t *testing.T) {
	server := miniredis.RunT(t)
	port, err := strconv.Atoi(server.Port())
	if err != nil {
		t.Fatalf("解析 miniredis 端口失败: %v", err)
	}
	newDriver := func(prefix string) *Redis {
		driver, createErr := NewRedis(map[string]interface{}{
			"host": server.Host(), "port": port, "prefix": prefix, "timeout_ms": redisTestTimeout.Milliseconds(),
		})
		if createErr != nil {
			t.Fatalf("创建命名空间驱动失败: %v", createErr)
		}
		t.Cleanup(func() { _ = driver.Close() })
		return driver
	}
	first := newDriver("thinkgo:test:first:")
	second := newDriver("thinkgo:test:second:")
	data := marshalRedisTestEnvelope(1, time.Now().Add(time.Minute))
	if err := first.Write("first", data); err != nil {
		t.Fatalf("写入第一个命名空间失败: %v", err)
	}
	if err := second.Write("second", data); err != nil {
		t.Fatalf("写入第二个命名空间失败: %v", err)
	}
	if err := first.Clear(); err != nil {
		t.Fatalf("清理第一个命名空间失败: %v", err)
	}
	if _, found, err := first.Read("first"); err != nil || found {
		t.Fatalf("第一个命名空间清理后仍有数据: found=%t err=%v", found, err)
	}
	if _, found, err := second.Read("second"); err != nil || !found {
		t.Fatalf("清理第一个命名空间误删第二个命名空间: found=%t err=%v", found, err)
	}
}

// TestRedisSessionLockOwnershipFailureWithMiniredis 验证锁所有权变化时写入必须失败闭环。
func TestRedisSessionLockOwnershipFailureWithMiniredis(t *testing.T) {
	driver, _ := newMiniredisSession(t)
	const sessionID = "ownership"
	err := driver.withSessionLock(sessionID, func(owner string) error {
		ctx, cancel := driver.context()
		defer cancel()
		if err := driver.client.Set(ctx, driver.lockKey(sessionID), "another-owner", redisSessionLockTTL).Err(); err != nil {
			return err
		}
		return driver.writeUnlocked(owner, sessionID, marshalRedisTestEnvelope(1, time.Now().Add(time.Minute)))
	})
	if !errors.Is(err, ErrSessionLockLost) {
		t.Fatalf("锁所有权变化后写入应失败: %v", err)
	}
	if _, found, readErr := driver.Read(sessionID); readErr != nil || found {
		t.Fatalf("失锁写入不应留下 Session: found=%t err=%v", found, readErr)
	}
}

// TestRedisSessionUpdateContextCancelsLockWait 验证请求取消会中断 Redis Session 锁等待。
func TestRedisSessionUpdateContextCancelsLockWait(t *testing.T) {
	driver, _ := newMiniredisSession(t)
	const sessionID = "context-cancel"
	lockOwner, err := redisSessionOwner()
	if err != nil {
		t.Fatalf("生成测试锁所有者失败: %v", err)
	}
	ctx, cancel := driver.context()
	if err := driver.client.Set(ctx, driver.lockKey(sessionID), lockOwner, redisSessionLockTTL).Err(); err != nil {
		cancel()
		t.Fatalf("预置 Redis Session 锁失败: %v", err)
	}
	cancel()

	requestContext, requestCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer requestCancel()
	callbackCalled := false
	err = driver.UpdateContext(requestContext, sessionID, func(string, bool) (string, bool, error) {
		callbackCalled = true
		return "", false, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("锁等待超时应返回 DeadlineExceeded，实际为 %v", err)
	}
	if callbackCalled {
		t.Fatal("请求上下文取消后不得执行 Session 更新回调")
	}
}

// TestRedisSessionUpdateBranchesWithMiniredis 验证更新回调错误、删除和无效数据的边界行为。
func TestRedisSessionUpdateBranchesWithMiniredis(t *testing.T) {
	driver, _ := newMiniredisSession(t)
	const sessionID = "update-branches"
	callbackErr := errors.New("回调失败")
	if err := driver.Update(sessionID, func(string, bool) (string, bool, error) {
		return "", false, callbackErr
	}); !errors.Is(err, callbackErr) {
		t.Fatalf("更新回调错误未透传: %v", err)
	}
	if err := driver.Update(sessionID, func(string, bool) (string, bool, error) {
		return "", true, nil
	}); err != nil {
		t.Fatalf("删除缺失 Session 不应失败: %v", err)
	}
	if err := driver.Write(sessionID, "not-json"); err == nil {
		t.Fatal("非法 Session 信封必须拒绝写入")
	}
	if err := driver.Write(sessionID, marshalRedisTestEnvelope(3, time.Now().Add(time.Minute))); err != nil {
		t.Fatalf("写入更新分支 Session 失败: %v", err)
	}
	if err := driver.Update(sessionID, func(string, bool) (string, bool, error) {
		return "", true, nil
	}); err != nil {
		t.Fatalf("更新回调请求删除失败: %v", err)
	}
	if _, found, err := driver.Read(sessionID); err != nil || found {
		t.Fatalf("更新删除后的 Session 状态错误: found=%t err=%v", found, err)
	}
	if err := driver.Delete(sessionID); err != nil {
		t.Fatalf("重复删除缺失 Session 不应失败: %v", err)
	}
}

// TestRedisSessionValidationBranches 验证客户端和 Session ID 校验不会触发网络访问。
func TestRedisSessionValidationBranches(t *testing.T) {
	var nilDriver *Redis
	if _, _, err := nilDriver.Read("valid"); !errors.Is(err, ErrInvalidRedisSessionClient) {
		t.Fatalf("空 Redis 驱动应返回客户端错误: %v", err)
	}
	driver := &Redis{}
	if err := driver.Write("valid", "{}"); !errors.Is(err, ErrInvalidRedisSessionClient) {
		t.Fatalf("未初始化 Redis 驱动应返回客户端错误: %v", err)
	}
	validDriver, _ := newMiniredisSession(t)
	if err := validDriver.Write("invalid/id", "{}"); !errors.Is(err, ErrInvalidSessionID) {
		t.Fatalf("非法 Session ID 应被拒绝: %v", err)
	}
}

// TestRedisSessionConfigValueValidation 覆盖端口、数据库和主机名解析的边界，避免配置绕过安全校验。
func TestRedisSessionConfigValueValidation(t *testing.T) {
	integerCases := []struct {
		name  string
		value interface{}
		want  int64
	}{
		{"int", int(1), 1}, {"int8", int8(2), 2}, {"int16", int16(3), 3},
		{"int32", int32(4), 4}, {"int64", int64(5), 5}, {"uint", uint(6), 6},
		{"uint8", uint8(7), 7}, {"uint16", uint16(8), 8}, {"uint32", uint32(9), 9},
		{"uint64", uint64(10), 10}, {"float", float64(11), 11}, {"json-number", json.Number("12"), 12},
	}
	for _, testCase := range integerCases {
		t.Run(testCase.name, func(t *testing.T) {
			value, err := redisSessionInteger(testCase.value)
			if err != nil || value != testCase.want {
				t.Fatalf("解析整数结果错误: value=%v want=%d err=%v", value, testCase.want, err)
			}
		})
	}
	for _, value := range []interface{}{
		uint64(math.MaxUint64), math.NaN(), math.Inf(1), 1.5, "12", nil,
	} {
		if _, err := redisSessionInteger(value); err == nil {
			t.Fatalf("非法整数值应被拒绝: %#v", value)
		}
	}

	hostCases := []struct {
		host  string
		valid bool
	}{
		{"127.0.0.1", true}, {"::1", true}, {"redis.example.com", true}, {"redis.example.com.", true},
		{"", false}, {"-redis.example.com", false}, {"redis-.example.com", false},
		{"redis..example.com", false}, {"redis_example.com", false}, {"redis\n.example.com", false},
		{strings.Repeat("a", 254), false},
	}
	for _, testCase := range hostCases {
		if validRedisSessionHost(testCase.host) != testCase.valid {
			t.Fatalf("主机名校验结果错误: host=%q want=%t", testCase.host, testCase.valid)
		}
	}

	driver, err := NewRedis(map[string]interface{}{
		"host": "redis.example.com", "port": 6379, "select": 1,
		"prefix": "thinkgo:test:tls:", "timeout_ms": 1000,
		"tls_enable": true, "tls_server_name": "redis.example.com", "tls_insecure_skip_verify": true,
	})
	if err != nil {
		t.Fatalf("合法 TLS 配置不应失败: %v", err)
	}
	if err := driver.Close(); err != nil {
		t.Fatalf("关闭配置测试驱动失败: %v", err)
	}
	for _, config := range []map[string]interface{}{
		{"prefix": "thinkgo:test:", "host": 1},
		{"prefix": "thinkgo:test:", "tls_enable": "true"},
		{"prefix": "thinkgo:test:", "tls_insecure_skip_verify": true},
		{"prefix": "thinkgo:test:", "timeout_ms": math.Inf(1)},
	} {
		if instance, createErr := NewRedis(config); instance != nil || !errors.Is(createErr, ErrInvalidRedisSessionConfig) {
			t.Fatalf("非法配置未被拒绝: config=%#v instance=%v err=%v", config, instance, createErr)
		}
	}
}

// TestRedisSessionGCValidation 验证无效客户端调用 GC 时立即失败。
func TestRedisSessionGCValidation(t *testing.T) {
	var nilDriver *Redis
	if count, err := nilDriver.GC(time.Minute); count != 0 || !errors.Is(err, ErrInvalidRedisSessionClient) {
		t.Fatalf("空 Redis 驱动 GC 结果错误: count=%d err=%v", count, err)
	}
}
