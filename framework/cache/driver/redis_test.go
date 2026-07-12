package driver

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestNewRedisStrictConfig 验证 Redis 地址、索引、超时、前缀和未知字段都在创建阶段严格校验。
func TestNewRedisStrictConfig(t *testing.T) {
	driver, err := NewRedis(map[string]interface{}{
		"host":       "::1",
		"port":       float64(6380),
		"password":   "secret",
		"select":     float64(2),
		"timeout_ms": float64(1500),
		"prefix":     "thinkgo:test:",
	})
	if err != nil {
		t.Fatalf("创建 Redis 驱动失败: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	if driver.client.Options().Addr != "[::1]:6380" || driver.client.Options().DB != 2 {
		t.Fatalf("Redis 地址或 DB 解析错误: addr=%q db=%d", driver.client.Options().Addr, driver.client.Options().DB)
	}
	if driver.opTimeout != 1500*time.Millisecond || driver.prefix != "thinkgo:test:" {
		t.Fatalf("Redis 超时或前缀解析错误: timeout=%v prefix=%q", driver.opTimeout, driver.prefix)
	}
	validPorts := []interface{}{
		int(6380), int16(6380), int32(6380), int64(6380),
		uint(6380), uint16(6380), uint32(6380), uint64(6380),
		float32(6380), json.Number("6380"),
	}
	for _, rawPort := range validPorts {
		instance, createErr := NewRedis(map[string]interface{}{"host": "localhost", "port": rawPort})
		if createErr != nil {
			t.Fatalf("合法端口类型 %#v 创建失败: %v", rawPort, createErr)
		}
		_ = instance.Close()
	}

	invalidConfigs := []map[string]interface{}{
		{"host": "localhost", "port": 1.5},
		{"host": "bad host", "port": float64(6379)},
		{"host": "localhost", "port": float64(0)},
		{"host": "localhost", "port": float64(6379), "select": float64(-1)},
		{"host": "localhost", "port": float64(6379), "timeout_ms": "slow"},
		{"host": "localhost", "port": float64(6379), "prefix": "bad\n"},
		{"host": "localhost", "port": float64(6379), "unknown": true},
		{"host": "localhost", "port": uint64(math.MaxUint64)},
		{"host": "localhost", "port": math.NaN()},
		{"host": "localhost", "port": float64(6379), "password": true},
		{"host": "localhost", "port": float64(6379), "allow_flush_db": "yes"},
		{"host": "localhost", "port": float64(6379), "prefix": strings.Repeat("x", maxRedisPrefixBytes+1)},
	}
	for _, config := range invalidConfigs {
		if instance, err := NewRedis(config); !errors.Is(err, ErrInvalidRedisConfig) || instance != nil {
			t.Fatalf("非法 Redis 配置 %#v 应失败: instance=%#v err=%v", config, instance, err)
		}
	}
}

// TestRedisClearRequiresExplicitUnprefixedPermission 验证空前缀不会默认执行危险的 FLUSHDB。
func TestRedisClearRequiresExplicitUnprefixedPermission(t *testing.T) {
	driver, err := NewRedis(map[string]interface{}{"host": "127.0.0.1", "port": float64(6379)})
	if err != nil {
		t.Fatalf("创建 Redis 驱动失败: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	if err := driver.Clear(); !errors.Is(err, ErrUnsafeRedisFlush) {
		t.Fatalf("空前缀 Clear 应返回 ErrUnsafeRedisFlush，实际为 %v", err)
	}
	if pattern := redisScanPattern(`app:*?[`); pattern != `app:\*\?\[*` {
		t.Fatalf("Redis SCAN 前缀未转义 glob 元字符: %q", pattern)
	}
}

type redisTestValue struct {
	value  string
	expiry time.Time
}

// redisTestServer 是测试专用的最小 RESP2 服务，覆盖驱动实际发送的命令和连接握手。
type redisTestServer struct {
	listener net.Listener
	lock     sync.Mutex
	values   map[string]redisTestValue
	conns    map[net.Conn]bool
	wait     sync.WaitGroup
	once     sync.Once
}

func newRedisTestServer(t *testing.T) *redisTestServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动测试 Redis 服务失败: %v", err)
	}
	server := &redisTestServer{
		listener: listener,
		values:   make(map[string]redisTestValue),
		conns:    make(map[net.Conn]bool),
	}
	server.wait.Add(1)
	go server.accept()
	t.Cleanup(server.Close)
	return server
}

func (s *redisTestServer) address(t *testing.T) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(s.listener.Addr().String())
	if err != nil {
		t.Fatalf("解析测试 Redis 地址失败: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("解析测试 Redis 端口失败: %v", err)
	}
	return host, port
}

func (s *redisTestServer) accept() {
	defer s.wait.Done()
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.lock.Lock()
		s.conns[connection] = true
		s.lock.Unlock()
		s.wait.Add(1)
		go s.serve(connection)
	}
}

func (s *redisTestServer) serve(connection net.Conn) {
	defer s.wait.Done()
	defer func() {
		s.lock.Lock()
		delete(s.conns, connection)
		s.lock.Unlock()
		_ = connection.Close()
	}()
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	for {
		command, err := readRedisTestCommand(reader)
		if err != nil {
			return
		}
		if err = s.execute(writer, command); err != nil {
			return
		}
		if err = writer.Flush(); err != nil {
			return
		}
	}
}

func readRedisTestCommand(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("RESP 命令必须是数组: %q", line)
	}
	count, err := strconv.Atoi(strings.TrimPrefix(line, "*"))
	if err != nil || count <= 0 {
		return nil, fmt.Errorf("RESP 命令长度非法: %q", line)
	}
	command := make([]string, 0, count)
	for index := 0; index < count; index++ {
		lengthLine, readErr := reader.ReadString('\n')
		if readErr != nil {
			return nil, readErr
		}
		lengthLine = strings.TrimSuffix(strings.TrimSuffix(lengthLine, "\n"), "\r")
		if !strings.HasPrefix(lengthLine, "$") {
			return nil, fmt.Errorf("RESP 参数必须是 bulk string: %q", lengthLine)
		}
		length, parseErr := strconv.Atoi(strings.TrimPrefix(lengthLine, "$"))
		if parseErr != nil || length < 0 {
			return nil, fmt.Errorf("RESP 参数长度非法: %q", lengthLine)
		}
		payload := make([]byte, length+2)
		if _, readErr = io.ReadFull(reader, payload); readErr != nil {
			return nil, readErr
		}
		if string(payload[length:]) != "\r\n" {
			return nil, errors.New("RESP 参数缺少结束符")
		}
		command = append(command, string(payload[:length]))
	}
	return command, nil
}

func (s *redisTestServer) execute(writer *bufio.Writer, command []string) error {
	name := strings.ToUpper(command[0])
	switch name {
	case "HELLO":
		return writeRedisTestError(writer, "ERR unknown command 'hello'")
	case "CLIENT", "SELECT", "AUTH", "PING":
		return writeRedisTestStatus(writer, "OK")
	case "GET":
		value, found := s.get(command[1])
		if !found {
			return writeRedisTestNil(writer)
		}
		return writeRedisTestBulk(writer, value.value)
	case "SET":
		return s.executeSet(writer, command)
	case "EXISTS":
		count := int64(0)
		for _, key := range command[1:] {
			if _, found := s.get(key); found {
				count++
			}
		}
		return writeRedisTestInteger(writer, count)
	case "DEL":
		return writeRedisTestInteger(writer, s.delete(command[1:]...))
	case "INCRBY", "DECRBY":
		return s.executeCounter(writer, command, name == "DECRBY")
	case "SCAN":
		return s.executeScan(writer, command)
	case "EVAL":
		return s.executeReleaseLock(writer, command)
	case "FLUSHDB":
		s.lock.Lock()
		count := int64(len(s.values))
		s.values = make(map[string]redisTestValue)
		s.lock.Unlock()
		return writeRedisTestStatusWithCount(writer, count)
	case "QUIT":
		return writeRedisTestStatus(writer, "OK")
	default:
		return writeRedisTestError(writer, "ERR unsupported test command")
	}
}

func (s *redisTestServer) executeSet(writer *bufio.Writer, command []string) error {
	if len(command) < 3 {
		return writeRedisTestError(writer, "ERR wrong number of arguments")
	}
	ttl := time.Duration(0)
	nx := false
	for index := 3; index < len(command); {
		switch strings.ToUpper(command[index]) {
		case "NX":
			nx = true
			index++
		case "PX":
			if index+1 >= len(command) {
				return writeRedisTestError(writer, "ERR missing PX value")
			}
			milliseconds, err := strconv.ParseInt(command[index+1], 10, 64)
			if err != nil || milliseconds <= 0 {
				return writeRedisTestError(writer, "ERR invalid expire time")
			}
			ttl = time.Duration(milliseconds) * time.Millisecond
			index += 2
		case "EX":
			if index+1 >= len(command) {
				return writeRedisTestError(writer, "ERR missing EX value")
			}
			seconds, err := strconv.ParseInt(command[index+1], 10, 64)
			if err != nil || seconds <= 0 {
				return writeRedisTestError(writer, "ERR invalid expire time")
			}
			ttl = time.Duration(seconds) * time.Second
			index += 2
		default:
			return writeRedisTestError(writer, "ERR unsupported SET option")
		}
	}
	s.lock.Lock()
	s.purgeExpiredLocked(command[1])
	if _, exists := s.values[command[1]]; nx && exists {
		s.lock.Unlock()
		return writeRedisTestNil(writer)
	}
	expiry := time.Time{}
	if ttl > 0 {
		expiry = time.Now().Add(ttl)
	}
	s.values[command[1]] = redisTestValue{value: command[2], expiry: expiry}
	s.lock.Unlock()
	return writeRedisTestStatus(writer, "OK")
}

func (s *redisTestServer) executeCounter(writer *bufio.Writer, command []string, subtract bool) error {
	step, err := strconv.ParseInt(command[2], 10, 64)
	if err != nil {
		return writeRedisTestError(writer, "ERR value is not an integer")
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	s.purgeExpiredLocked(command[1])
	stored := s.values[command[1]]
	current := int64(0)
	if stored.value != "" {
		current, err = strconv.ParseInt(stored.value, 10, 64)
		if err != nil {
			return writeRedisTestError(writer, "ERR value is not an integer")
		}
	}
	updated := current
	if subtract {
		updated, err = checkedCounterSubtract(current, step)
	} else {
		updated, err = checkedCounterAdd(current, step)
	}
	if err != nil {
		return writeRedisTestError(writer, "ERR increment or decrement would overflow")
	}
	s.values[command[1]] = redisTestValue{value: strconv.FormatInt(updated, 10), expiry: stored.expiry}
	return writeRedisTestInteger(writer, updated)
}

func (s *redisTestServer) executeScan(writer *bufio.Writer, command []string) error {
	pattern := "*"
	for index := 2; index+1 < len(command); index += 2 {
		if strings.EqualFold(command[index], "MATCH") {
			pattern = command[index+1]
		}
	}
	s.lock.Lock()
	keys := make([]string, 0, len(s.values))
	for key := range s.values {
		s.purgeExpiredLocked(key)
		if _, exists := s.values[key]; !exists {
			continue
		}
		matched, err := path.Match(pattern, key)
		if err == nil && matched {
			keys = append(keys, key)
		}
	}
	s.lock.Unlock()
	if _, err := writer.WriteString("*2\r\n$1\r\n0\r\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "*%d\r\n", len(keys)); err != nil {
		return err
	}
	for _, key := range keys {
		if err := writeRedisTestBulk(writer, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *redisTestServer) executeReleaseLock(writer *bufio.Writer, command []string) error {
	if len(command) < 5 {
		return writeRedisTestError(writer, "ERR invalid EVAL")
	}
	key := command[3]
	owner := command[4]
	s.lock.Lock()
	s.purgeExpiredLocked(key)
	stored, exists := s.values[key]
	removed := int64(0)
	if exists && stored.value == owner {
		delete(s.values, key)
		removed = 1
	}
	s.lock.Unlock()
	return writeRedisTestInteger(writer, removed)
}

func (s *redisTestServer) get(key string) (redisTestValue, bool) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.purgeExpiredLocked(key)
	value, found := s.values[key]
	return value, found
}

func (s *redisTestServer) delete(keys ...string) int64 {
	s.lock.Lock()
	defer s.lock.Unlock()
	count := int64(0)
	for _, key := range keys {
		s.purgeExpiredLocked(key)
		if _, exists := s.values[key]; exists {
			delete(s.values, key)
			count++
		}
	}
	return count
}

func (s *redisTestServer) setRaw(key, value string) {
	s.lock.Lock()
	s.values[key] = redisTestValue{value: value}
	s.lock.Unlock()
}

func (s *redisTestServer) purgeExpiredLocked(key string) {
	value, exists := s.values[key]
	if exists && !value.expiry.IsZero() && !time.Now().Before(value.expiry) {
		delete(s.values, key)
	}
}

func (s *redisTestServer) Close() {
	s.once.Do(func() {
		_ = s.listener.Close()
		s.lock.Lock()
		for connection := range s.conns {
			_ = connection.Close()
		}
		s.lock.Unlock()
		s.wait.Wait()
	})
}

func writeRedisTestStatus(writer io.Writer, status string) error {
	_, err := fmt.Fprintf(writer, "+%s\r\n", status)
	return err
}

func writeRedisTestStatusWithCount(writer io.Writer, _ int64) error {
	return writeRedisTestStatus(writer, "OK")
}

func writeRedisTestError(writer io.Writer, message string) error {
	_, err := fmt.Fprintf(writer, "-%s\r\n", message)
	return err
}

func writeRedisTestNil(writer io.Writer) error {
	_, err := io.WriteString(writer, "$-1\r\n")
	return err
}

func writeRedisTestBulk(writer io.Writer, value string) error {
	_, err := fmt.Fprintf(writer, "$%d\r\n%s\r\n", len(value), value)
	return err
}

func writeRedisTestInteger(writer io.Writer, value int64) error {
	_, err := fmt.Fprintf(writer, ":%d\r\n", value)
	return err
}

// TestRedisDriverRoundTripCounterLocksAndClear 验证真实 RESP 流程、TTL、计数、锁和按前缀清理语义。
func TestRedisDriverRoundTripCounterLocksAndClear(t *testing.T) {
	server := newRedisTestServer(t)
	host, port := server.address(t)
	driver, err := NewRedis(map[string]interface{}{
		"host": host, "port": port, "prefix": "app:", "timeout_ms": 500,
	})
	if err != nil {
		t.Fatalf("创建 Redis 驱动失败: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	if err = driver.Set("nil", nil, 0); err != nil {
		t.Fatalf("写入 Redis nil 失败: %v", err)
	}
	if value, found, getErr := driver.Get("nil"); getErr != nil || !found || value != nil {
		t.Fatalf("Redis nil 命中错误: value=%#v found=%t err=%v", value, found, getErr)
	}
	if exists, hasErr := driver.Has("nil"); hasErr != nil || !exists {
		t.Fatalf("Redis Has 未识别 nil: exists=%t err=%v", exists, hasErr)
	}
	if err = driver.Set("counter", int64(5), 80*time.Millisecond); err != nil {
		t.Fatalf("写入 Redis 计数失败: %v", err)
	}
	if value, changeErr := driver.Inc("counter", 3); changeErr != nil || value != 8 {
		t.Fatalf("Redis Inc 错误: value=%d err=%v", value, changeErr)
	}
	if value, changeErr := driver.Dec("counter", 2); changeErr != nil || value != 6 {
		t.Fatalf("Redis Dec 错误: value=%d err=%v", value, changeErr)
	}
	if _, changeErr := driver.Inc("counter", -1); !errors.Is(changeErr, ErrInvalidCounterStep) {
		t.Fatalf("Redis 负计数步长应返回 ErrInvalidCounterStep，实际为 %v", changeErr)
	}
	time.Sleep(120 * time.Millisecond)
	if _, found, getErr := driver.Get("counter"); getErr != nil || found {
		t.Fatalf("Redis 计数操作不应移除 TTL: found=%t err=%v", found, getErr)
	}
	server.setRaw("app:corrupt", "not-json")
	if _, _, getErr := driver.Get("corrupt"); !errors.Is(getErr, ErrCorruptCacheEntry) {
		t.Fatalf("损坏 Redis 值应显式报错，实际为 %v", getErr)
	}
	if err = driver.Set("unsupported", make(chan int), 0); err == nil {
		t.Fatal("不可 JSON 序列化的值不得写入 Redis")
	}

	lockKey := "__thinkgo_lock__:job"
	if _, lockErr := driver.AcquireLock(lockKey, "", time.Minute); !errors.Is(lockErr, ErrInvalidCacheLock) {
		t.Fatalf("空 owner 应返回 ErrInvalidCacheLock，实际为 %v", lockErr)
	}
	if _, lockErr := driver.ReleaseLock(lockKey, ""); !errors.Is(lockErr, ErrInvalidCacheLock) {
		t.Fatalf("空 owner 释放应返回 ErrInvalidCacheLock，实际为 %v", lockErr)
	}
	if acquired, lockErr := driver.AcquireLock(lockKey, "owner-a", time.Minute); lockErr != nil || !acquired {
		t.Fatalf("获取 Redis 锁失败: acquired=%t err=%v", acquired, lockErr)
	}
	if acquired, lockErr := driver.AcquireLock(lockKey, "owner-b", time.Minute); lockErr != nil || acquired {
		t.Fatalf("锁持有期间其他 owner 不应获取: acquired=%t err=%v", acquired, lockErr)
	}
	if released, lockErr := driver.ReleaseLock(lockKey, "owner-b"); lockErr != nil || released {
		t.Fatalf("错误 owner 不得释放 Redis 锁: released=%t err=%v", released, lockErr)
	}
	if err = driver.Set("business", "value", 0); err != nil {
		t.Fatalf("写入待清理业务键失败: %v", err)
	}
	if err = driver.Clear(); err != nil {
		t.Fatalf("按前缀清理 Redis 失败: %v", err)
	}
	if _, found, getErr := driver.Get("business"); getErr != nil || found {
		t.Fatalf("Redis Clear 后业务键仍存在: found=%t err=%v", found, getErr)
	}
	if released, lockErr := driver.ReleaseLock(lockKey, "owner-a"); lockErr != nil || !released {
		t.Fatalf("Redis Clear 不得释放现有业务锁: released=%t err=%v", released, lockErr)
	}
	if err = driver.Delete("nil"); err != nil {
		t.Fatalf("删除 Redis 键失败: %v", err)
	}
}

// TestRedisUnprefixedExplicitFlushAndClosedErrors 验证显式 FLUSHDB 授权及关闭后的错误传播。
func TestRedisUnprefixedExplicitFlushAndClosedErrors(t *testing.T) {
	server := newRedisTestServer(t)
	host, port := server.address(t)
	driver, err := NewRedis(map[string]interface{}{
		"host": host, "port": port, "allow_flush_db": true, "timeout_ms": 500,
	})
	if err != nil {
		t.Fatalf("创建无前缀 Redis 驱动失败: %v", err)
	}
	if err = driver.Set("key", "value", 0); err != nil {
		t.Fatalf("写入无前缀 Redis 失败: %v", err)
	}
	if err = driver.Clear(); err != nil {
		t.Fatalf("显式授权 FLUSHDB 后清理失败: %v", err)
	}
	if _, found, getErr := driver.Get("key"); getErr != nil || found {
		t.Fatalf("FLUSHDB 后键仍存在: found=%t err=%v", found, getErr)
	}
	if err = driver.Close(); err != nil {
		t.Fatalf("关闭 Redis 驱动失败: %v", err)
	}
	if err = driver.Close(); err != nil {
		t.Fatalf("重复关闭 Redis 驱动应幂等: %v", err)
	}
	if _, _, err = driver.Get("closed"); err == nil {
		t.Fatal("关闭后 Redis 操作必须返回错误")
	}
}

// TestRedisZeroValueReturnsExplicitErrors 验证零值或 nil Redis 驱动不会因空客户端发生 panic。
func TestRedisZeroValueReturnsExplicitErrors(t *testing.T) {
	assertInvalid := func(name string, operation func() error) {
		t.Helper()
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("%s 不应 panic: %v", name, recovered)
			}
		}()
		if err := operation(); !errors.Is(err, ErrInvalidRedisClient) {
			t.Fatalf("%s 应返回 ErrInvalidRedisClient，实际为 %v", name, err)
		}
	}
	zero := &Redis{}
	assertInvalid("Get", func() error {
		_, _, err := zero.Get("key")
		return err
	})
	assertInvalid("Set", func() error { return zero.Set("key", "value", 0) })
	assertInvalid("Has", func() error {
		_, err := zero.Has("key")
		return err
	})
	assertInvalid("Delete", func() error { return zero.Delete("key") })
	assertInvalid("Clear", zero.Clear)
	assertInvalid("Inc", func() error {
		_, err := zero.Inc("key", 1)
		return err
	})
	assertInvalid("Dec", func() error {
		_, err := zero.Dec("key", 1)
		return err
	})
	assertInvalid("AcquireLock", func() error {
		_, err := zero.AcquireLock("key", "owner", time.Second)
		return err
	})
	assertInvalid("ReleaseLock", func() error {
		_, err := zero.ReleaseLock("key", "owner")
		return err
	})
	var nilDriver *Redis
	assertInvalid("nil Get", func() error {
		_, _, err := nilDriver.Get("key")
		return err
	})
}
