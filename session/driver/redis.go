package driver

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	defaultRedisSessionHost      = "127.0.0.1"
	defaultRedisSessionPort      = 6379
	defaultRedisSessionTimeout   = 3 * time.Second
	maxRedisSessionTimeout       = 60 * time.Second
	maxRedisSessionPrefixBytes   = 256
	redisSessionLockTTL          = 2 * maxRedisSessionTimeout
	redisSessionLockRenew        = redisSessionLockTTL / 3
	redisSessionLockWait         = 15 * time.Second
	redisSessionLockRetry        = 5 * time.Millisecond
	maxRedisSessionDatabase      = 1024
	redisSessionDataPrefix       = "data:"
	redisSessionLockPrefix       = "lock:"
	redisSessionReleaseLuaScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`
	redisSessionRenewLuaScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`
	redisSessionWriteLuaScript = `
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
	return 0
end
if ARGV[3] == "0" then
	redis.call("SET", KEYS[2], ARGV[2])
else
	redis.call("SET", KEYS[2], ARGV[2], "PX", ARGV[3])
end
return 1
`
	redisSessionDeleteLuaScript = `
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
	return -1
end
return redis.call("DEL", KEYS[2])
`
)

var (
	// ErrInvalidRedisSessionConfig 表示 Redis Session 的连接、命名空间或安全配置非法。
	ErrInvalidRedisSessionConfig = errors.New("Redis Session 配置非法")
	// ErrInvalidRedisSessionClient 表示 Redis Session 客户端未正确创建。
	ErrInvalidRedisSessionClient = errors.New("Redis Session 客户端非法")
	// ErrSessionLockLost 表示 Session 更新期间无法续租分布式锁。
	ErrSessionLockLost = errors.New("Redis Session 锁租约丢失")
	// ErrInvalidRedisSessionContext 表示 Redis Session 操作上下文为空。
	ErrInvalidRedisSessionContext = errors.New("Redis Session 上下文无效")
)

type redisSessionEnvelope struct {
	ExpireAt int64 `json:"expire_at"`
}

// Redis 是使用 Redis TTL 和按 Session ID 分布式锁的高并发 Session 驱动。
type Redis struct {
	client    *redis.Client
	prefix    string
	timeout   time.Duration
	closeOnce sync.Once
	closeErr  error
}

// NewRedis 创建 Redis Session 驱动；prefix 必须非空，避免清理操作触碰其它业务键。
func NewRedis(config map[string]interface{}) (*Redis, error) {
	allowed := map[string]struct{}{
		"host": {}, "port": {}, "password": {}, "select": {}, "prefix": {},
		"timeout_ms": {}, "tls_enable": {}, "tls_server_name": {},
		"tls_insecure_skip_verify": {},
	}
	for key := range config {
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("%w: 未知配置项 %q", ErrInvalidRedisSessionConfig, key)
		}
	}

	host := defaultRedisSessionHost
	if raw, ok := config["host"]; ok {
		value, valid := raw.(string)
		if !valid || !validRedisSessionHost(value) {
			return nil, fmt.Errorf("%w: host 非法", ErrInvalidRedisSessionConfig)
		}
		host = value
	}
	port := int64(defaultRedisSessionPort)
	if raw, ok := config["port"]; ok {
		value, err := redisSessionInteger(raw)
		if err != nil || value < 1 || value > 65535 {
			return nil, fmt.Errorf("%w: port 非法", ErrInvalidRedisSessionConfig)
		}
		port = value
	}
	database := int64(0)
	if raw, ok := config["select"]; ok {
		value, err := redisSessionInteger(raw)
		if err != nil || value < 0 || value > maxRedisSessionDatabase {
			return nil, fmt.Errorf("%w: select 非法", ErrInvalidRedisSessionConfig)
		}
		database = value
	}
	timeout := defaultRedisSessionTimeout
	if raw, ok := config["timeout_ms"]; ok {
		value, err := redisSessionInteger(raw)
		if err != nil || value <= 0 || value > maxRedisSessionTimeout.Milliseconds() {
			return nil, fmt.Errorf("%w: timeout_ms 非法", ErrInvalidRedisSessionConfig)
		}
		timeout = time.Duration(value) * time.Millisecond
	}
	prefix, _ := config["prefix"].(string)
	if prefix == "" || len(prefix) > maxRedisSessionPrefixBytes || containsRedisSessionControl(prefix) || strings.ContainsAny(prefix, "*?[]\\") {
		return nil, fmt.Errorf("%w: prefix 必须是非空且不含 Redis 通配符的字符串", ErrInvalidRedisSessionConfig)
	}
	password := ""
	if raw, ok := config["password"]; ok {
		value, valid := raw.(string)
		if !valid {
			return nil, fmt.Errorf("%w: password 必须是字符串", ErrInvalidRedisSessionConfig)
		}
		password = value
	}

	tlsEnable := false
	if raw, ok := config["tls_enable"]; ok {
		value, valid := raw.(bool)
		if !valid {
			return nil, fmt.Errorf("%w: tls_enable 必须是布尔值", ErrInvalidRedisSessionConfig)
		}
		tlsEnable = value
	}
	tlsServerName := ""
	if raw, ok := config["tls_server_name"]; ok {
		value, valid := raw.(string)
		if !valid {
			return nil, fmt.Errorf("%w: tls_server_name 必须是字符串", ErrInvalidRedisSessionConfig)
		}
		tlsServerName = strings.TrimSpace(value)
	}
	tlsInsecureSkipVerify := false
	if raw, ok := config["tls_insecure_skip_verify"]; ok {
		value, valid := raw.(bool)
		if !valid {
			return nil, fmt.Errorf("%w: tls_insecure_skip_verify 必须是布尔值", ErrInvalidRedisSessionConfig)
		}
		tlsInsecureSkipVerify = value
	}
	if !tlsEnable && (tlsServerName != "" || tlsInsecureSkipVerify) {
		return nil, fmt.Errorf("%w: TLS 选项必须在 tls_enable=true 时使用", ErrInvalidRedisSessionConfig)
	}
	var tlsConfig *tls.Config
	if tlsEnable {
		tlsConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         tlsServerName,
			InsecureSkipVerify: tlsInsecureSkipVerify,
		}
	}

	return &Redis{
		client: redis.NewClient(&redis.Options{
			ContextTimeoutEnabled: true,
			Addr:                  net.JoinHostPort(host, strconv.FormatInt(port, 10)),
			Password:              password,
			DB:                    int(database),
			DialTimeout:           timeout,
			ReadTimeout:           timeout,
			WriteTimeout:          timeout,
			TLSConfig:             tlsConfig,
		}),
		prefix:  prefix,
		timeout: timeout,
	}, nil
}

// Read 读取 Redis 中的 Session 原文，并区分缺失值。
func (r *Redis) Read(id string) (string, bool, error) {
	return r.ReadContext(context.Background(), id)
}

// ReadContext 读取 Redis Session，并继承调用方的取消信号。
func (r *Redis) ReadContext(parent context.Context, id string) (string, bool, error) {
	if parent == nil {
		return "", false, ErrInvalidRedisSessionContext
	}
	if err := r.validate(id); err != nil {
		return "", false, err
	}
	ctx, cancel := r.contextWithParent(parent)
	defer cancel()
	data, err := r.client.Get(ctx, r.dataKey(id)).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return data, true, nil
}

// Write 在 Redis 中写入 Session，并从信封中的 expire_at 派生原生 TTL。
func (r *Redis) Write(id string, data string) error {
	if err := r.validate(id); err != nil {
		return err
	}
	return r.withSessionLock(id, func(owner string) error {
		return r.writeUnlocked(owner, id, data)
	})
}

// Delete 删除单个 Session；不存在时保持幂等成功。
func (r *Redis) Delete(id string) error {
	if err := r.validate(id); err != nil {
		return err
	}
	return r.withSessionLock(id, func(owner string) error {
		ctx, cancel := r.context()
		defer cancel()
		result, err := r.client.Eval(ctx, redisSessionDeleteLuaScript, []string{r.lockKey(id), r.dataKey(id)}, owner).Result()
		if err != nil {
			return err
		}
		if deleted, ok := result.(int64); !ok || deleted < 0 {
			return ErrSessionLockLost
		}
		return nil
	})
}

// Clear 只扫描并删除当前 Session 命名空间，不执行 Redis 整库清理。
func (r *Redis) Clear() error {
	if err := r.validateClient(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout*5)
	defer cancel()
	var cursor uint64
	pattern := r.prefix + redisSessionDataPrefix + "*"
	for {
		keys, next, err := r.client.Scan(ctx, cursor, pattern, 256).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err = r.client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

// Update 在 Redis 分布式锁内执行一次读改写，保证并发请求不会覆盖字段增量。
func (r *Redis) Update(id string, update func(data string, found bool) (next string, remove bool, err error)) error {
	return r.UpdateContext(context.Background(), id, update)
}

// UpdateContext 在 Redis 分布式锁内执行可取消的读改写。
func (r *Redis) UpdateContext(parent context.Context, id string, update func(data string, found bool) (next string, remove bool, err error)) error {
	if parent == nil {
		return ErrInvalidRedisSessionContext
	}
	if err := r.validate(id); err != nil {
		return err
	}
	if update == nil {
		return ErrInvalidSessionUpdate
	}
	return r.withSessionLockContext(parent, id, func(owner string) error {
		ctx, cancel := r.contextWithParent(parent)
		current, err := r.client.Get(ctx, r.dataKey(id)).Result()
		cancel()
		found := true
		if errors.Is(err, redis.Nil) {
			current = ""
			found = false
		} else if err != nil {
			return err
		}
		next, remove, err := update(current, found)
		if err != nil {
			return err
		}
		if remove {
			ctx, cancel = r.contextWithParent(parent)
			defer cancel()
			result, deleteErr := r.client.Eval(ctx, redisSessionDeleteLuaScript, []string{r.lockKey(id), r.dataKey(id)}, owner).Result()
			if deleteErr != nil {
				return deleteErr
			}
			if deleted, ok := result.(int64); !ok || deleted < 0 {
				return ErrSessionLockLost
			}
			return nil
		}
		return r.writeUnlockedContext(parent, owner, id, next)
	})
}

// GC 依赖 Redis 原生 TTL，避免应用层扫描全部 Session。
func (r *Redis) GC(time.Duration) (int, error) {
	if err := r.validateClient(); err != nil {
		return 0, err
	}
	return 0, nil
}

// Close 幂等关闭 Redis 连接池。
func (r *Redis) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.closeErr = r.client.Close()
	})
	return r.closeErr
}

func (r *Redis) writeUnlocked(owner, id string, data string) error {
	return r.writeUnlockedContext(context.Background(), owner, id, data)
}

func (r *Redis) writeUnlockedContext(parent context.Context, owner, id string, data string) error {
	ttl, err := redisSessionTTL(data)
	if err != nil {
		return err
	}
	ctx, cancel := r.contextWithParent(parent)
	defer cancel()
	ttlMilliseconds := int64(0)
	if ttl > 0 {
		ttlMilliseconds = ttl.Milliseconds()
		if ttlMilliseconds < 1 {
			ttlMilliseconds = 1
		}
	}
	result, err := r.client.Eval(ctx, redisSessionWriteLuaScript, []string{r.lockKey(id), r.dataKey(id)}, owner, data, ttlMilliseconds).Result()
	if err != nil {
		return err
	}
	if written, ok := result.(int64); !ok || written != 1 {
		return ErrSessionLockLost
	}
	return nil
}

func (r *Redis) withSessionLock(id string, callback func(string) error) error {
	return r.withSessionLockContext(context.Background(), id, callback)
}

func (r *Redis) withSessionLockContext(parent context.Context, id string, callback func(string) error) error {
	if parent == nil {
		return ErrInvalidRedisSessionContext
	}
	owner, err := redisSessionOwner()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(redisSessionLockWait)
	lockKey := r.lockKey(id)
	for {
		ctx, cancel := r.contextWithParent(parent)
		acquired, acquireErr := r.client.SetNX(ctx, lockKey, owner, redisSessionLockTTL).Result()
		cancel()
		if acquireErr != nil {
			return acquireErr
		}
		if acquired {
			break
		}
		if !time.Now().Before(deadline) {
			return ErrSessionLockTimeout
		}
		timer := time.NewTimer(redisSessionLockRetry)
		select {
		case <-timer.C:
		case <-parent.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return parent.Err()
		}
	}
	stopRenewal := make(chan struct{})
	renewalFinished := make(chan struct{})
	renewalErrors := make(chan error, 1)
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopRenewal) }) }
	go func() {
		defer close(renewalFinished)
		ticker := time.NewTicker(redisSessionLockRenew)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := r.context()
				result, renewErr := r.client.Eval(ctx, redisSessionRenewLuaScript, []string{lockKey}, owner, redisSessionLockTTL.Milliseconds()).Result()
				cancel()
				if renewErr != nil {
					renewalErrors <- errors.Join(ErrSessionLockLost, renewErr)
					return
				}
				if renewed, ok := result.(int64); !ok || renewed != 1 {
					renewalErrors <- ErrSessionLockLost
					return
				}
			case <-stopRenewal:
				return
			}
		}
	}()
	// 释放操作使用原子脚本，只会删除仍属于当前 owner 的锁；即使回调 panic 也会停止续租。
	defer func() {
		stop()
		<-renewalFinished
		ctx, cancel := r.context()
		_, _ = r.client.Eval(ctx, redisSessionReleaseLuaScript, []string{lockKey}, owner).Result()
		cancel()
	}()
	callbackErr := callback(owner)
	stop()
	<-renewalFinished
	select {
	case renewalErr := <-renewalErrors:
		callbackErr = errors.Join(callbackErr, renewalErr)
	default:
	}
	return callbackErr
}

func (r *Redis) validate(id string) error {
	if err := r.validateClient(); err != nil {
		return err
	}
	return validateSessionID(id)
}

func (r *Redis) validateClient() error {
	if r == nil || r.client == nil || r.timeout <= 0 || r.prefix == "" {
		return ErrInvalidRedisSessionClient
	}
	return nil
}

func (r *Redis) context() (context.Context, context.CancelFunc) {
	return r.contextWithParent(context.Background())
}

func (r *Redis) contextWithParent(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, r.timeout)
}

func (r *Redis) dataKey(id string) string {
	return r.prefix + redisSessionDataPrefix + id
}

func (r *Redis) lockKey(id string) string {
	return r.prefix + redisSessionLockPrefix + id
}

func redisSessionTTL(data string) (time.Duration, error) {
	var envelope redisSessionEnvelope
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		return 0, fmt.Errorf("session 信封非法: %w", err)
	}
	if envelope.ExpireAt <= 0 {
		return 0, nil
	}
	ttl := time.Until(time.Unix(envelope.ExpireAt, 0))
	if ttl <= 0 {
		return time.Millisecond, nil
	}
	return ttl, nil
}

func redisSessionOwner() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("生成 Redis Session 锁 owner 失败: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func redisSessionInteger(value interface{}) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, ErrInvalidRedisSessionConfig
		}
		return int64(typed), nil
	case uint8:
		return int64(typed), nil
	case uint16:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		if typed > math.MaxInt64 {
			return 0, ErrInvalidRedisSessionConfig
		}
		return int64(typed), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed < math.MinInt64 || typed > math.MaxInt64 {
			return 0, ErrInvalidRedisSessionConfig
		}
		return int64(typed), nil
	case json.Number:
		return strconv.ParseInt(typed.String(), 10, 64)
	default:
		return 0, ErrInvalidRedisSessionConfig
	}
}

func validRedisSessionHost(host string) bool {
	if host == "" || len(host) > 253 || containsRedisSessionControl(host) || strings.ContainsAny(host, " \t\r\n") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	trimmed := strings.TrimSuffix(host, ".")
	if trimmed == "" {
		return false
	}
	for _, label := range strings.Split(trimmed, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func containsRedisSessionControl(value string) bool {
	return strings.ContainsAny(value, "\x00\r\n\t")
}
