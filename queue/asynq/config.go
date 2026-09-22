// Package asynq 使用 Redis 持久化实现 ThinkGo 队列的生产者、工作进程和周期调度器。
package asynq

import (
	"crypto/tls"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"

	backend "github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/zhuhanxin0308/thinkgo/v3/queue"
)

const (
	maximumRedisDatabase   = 1024
	maximumRedisPoolSize   = 10_000
	maximumRedisTimeout    = time.Minute
	maximumCredentialBytes = 4096
)

// RedisConfig 描述单节点 Redis 连接；集群和哨兵应使用 FromRedisClient 构造入口。
type RedisConfig struct {
	Address      string
	Username     string
	Password     string
	DB           int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	PoolSize     int
	TLSConfig    *tls.Config
}

// Options 严格校验地址、凭据、超时和 TLS 后返回 Asynq 连接选项。
func (config RedisConfig) Options() (backend.RedisClientOpt, error) {
	address := strings.TrimSpace(config.Address)
	host, portText, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" {
		return backend.RedisClientOpt{}, fmt.Errorf("%w: Redis 地址非法", queue.ErrBackendUnavailable)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return backend.RedisClientOpt{}, fmt.Errorf("%w: Redis 端口非法", queue.ErrBackendUnavailable)
	}
	if config.DB < 0 || config.DB > maximumRedisDatabase {
		return backend.RedisClientOpt{}, fmt.Errorf("%w: Redis DB 超出范围", queue.ErrBackendUnavailable)
	}
	if len(config.Username) > maximumCredentialBytes || len(config.Password) > maximumCredentialBytes || strings.ContainsAny(config.Username+config.Password, "\r\n\x00") {
		return backend.RedisClientOpt{}, fmt.Errorf("%w: Redis 凭据非法", queue.ErrBackendUnavailable)
	}
	if !validRedisTimeout(config.DialTimeout) || !validRedisTimeout(config.ReadTimeout) || !validRedisTimeout(config.WriteTimeout) {
		return backend.RedisClientOpt{}, fmt.Errorf("%w: Redis 超时非法", queue.ErrBackendUnavailable)
	}
	if config.PoolSize < 0 || config.PoolSize > maximumRedisPoolSize {
		return backend.RedisClientOpt{}, fmt.Errorf("%w: Redis 连接池大小非法", queue.ErrBackendUnavailable)
	}
	var tlsConfig *tls.Config
	if config.TLSConfig != nil {
		tlsConfig = config.TLSConfig.Clone()
		if tlsConfig.MinVersion == 0 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
		if tlsConfig.MinVersion < tls.VersionTLS12 {
			return backend.RedisClientOpt{}, fmt.Errorf("%w: Redis TLS 最低版本不能低于 1.2", queue.ErrBackendUnavailable)
		}
	}
	return backend.RedisClientOpt{
		Network:      "tcp",
		Addr:         net.JoinHostPort(host, portText),
		Username:     config.Username,
		Password:     config.Password,
		DB:           config.DB,
		DialTimeout:  config.DialTimeout,
		ReadTimeout:  config.ReadTimeout,
		WriteTimeout: config.WriteTimeout,
		PoolSize:     config.PoolSize,
		TLSConfig:    tlsConfig,
	}, nil
}

func redisOptions(config RedisConfig) (*redis.Options, error) {
	options, err := config.Options()
	if err != nil {
		return nil, err
	}
	return &redis.Options{
		Network:      options.Network,
		Addr:         options.Addr,
		Username:     options.Username,
		Password:     options.Password,
		DB:           options.DB,
		DialTimeout:  options.DialTimeout,
		ReadTimeout:  options.ReadTimeout,
		WriteTimeout: options.WriteTimeout,
		PoolSize:     options.PoolSize,
		TLSConfig:    options.TLSConfig,
	}, nil
}

func validRedisTimeout(value time.Duration) bool {
	return value >= 0 && value <= maximumRedisTimeout
}

func validateRedisClient(client redis.UniversalClient) error {
	if client == nil {
		return queue.ErrBackendUnavailable
	}
	value := reflect.ValueOf(client)
	if value.Kind() == reflect.Ptr && value.IsNil() {
		return queue.ErrBackendUnavailable
	}
	return nil
}

func backendOptions(options queue.EnqueueOptions, now time.Time) ([]backend.Option, error) {
	if err := options.Validate(now); err != nil {
		return nil, err
	}
	result := []backend.Option{
		backend.Queue(options.NormalizedQueue()),
		backend.MaxRetry(options.EffectiveMaxRetry()),
		backend.Timeout(options.EffectiveTimeout()),
	}
	if !options.ProcessAt.IsZero() {
		result = append(result, backend.ProcessAt(options.ProcessAt))
	}
	if options.UniqueFor > 0 {
		result = append(result, backend.Unique(options.UniqueFor))
	}
	if options.Retention > 0 {
		result = append(result, backend.Retention(options.Retention))
	}
	return result, nil
}

func backendTask(task queue.Task) (*backend.Task, error) {
	// Task 的字段不可修改，构造器已经完整校验；这里只需拒绝未构造的零值。
	if task.Type() == "" {
		return nil, queue.ErrInvalidTask
	}
	return backend.NewTaskWithHeaders(task.Type(), task.Payload(), task.Headers()), nil
}
