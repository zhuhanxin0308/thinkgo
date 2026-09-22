package asynq

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	backend "github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/zhuhanxin0308/thinkgo/framework/queue"
)

// Producer 把任务持久化到 Asynq Redis 队列。
type Producer struct {
	mu        sync.RWMutex
	client    *backend.Client
	owned     bool
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

// NewProducer 创建拥有独立 Redis 连接池的生产者。
func NewProducer(config RedisConfig) (*Producer, error) {
	options, err := config.Options()
	if err != nil {
		return nil, err
	}
	return &Producer{client: backend.NewClient(options), owned: true}, nil
}

// NewProducerFromRedisClient 复用调用方管理生命周期的 Redis 客户端。
func NewProducerFromRedisClient(client redis.UniversalClient) (*Producer, error) {
	if err := validateRedisClient(client); err != nil {
		return nil, err
	}
	return &Producer{client: backend.NewClientFromRedisClient(client)}, nil
}

// Enqueue 校验并原子持久化任务；重复唯一任务返回 queue.ErrDuplicateTask。
func (producer *Producer) Enqueue(ctx context.Context, task queue.Task, options queue.EnqueueOptions) (queue.Info, error) {
	if producer == nil || producer.client == nil || ctx == nil {
		return queue.Info{}, queue.ErrBackendUnavailable
	}
	// 关闭等待已经接受的入队操作完成，随后原子封闭新的入队入口。
	producer.mu.RLock()
	defer producer.mu.RUnlock()
	if producer.closed {
		return queue.Info{}, fmt.Errorf("%w: %w", queue.ErrBackendUnavailable, queue.ErrClosed)
	}
	backendTaskValue, err := backendTask(task)
	if err != nil {
		return queue.Info{}, err
	}
	resolvedOptions, err := backendOptions(options, time.Now())
	if err != nil {
		return queue.Info{}, err
	}
	info, err := producer.client.EnqueueContext(ctx, backendTaskValue, resolvedOptions...)
	if err != nil {
		if errors.Is(err, backend.ErrDuplicateTask) {
			return queue.Info{}, fmt.Errorf("%w: %s", queue.ErrDuplicateTask, task.Type())
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return queue.Info{}, contextErr
		}
		return queue.Info{}, fmt.Errorf("%w: %v", queue.ErrBackendUnavailable, err)
	}
	return queue.Info{ID: info.ID, Queue: info.Queue, Type: info.Type}, nil
}

// Close 幂等关闭生产者；复用的 Redis 客户端仍由调用方关闭。
func (producer *Producer) Close() error {
	if producer == nil || producer.client == nil {
		return nil
	}
	producer.closeOnce.Do(func() {
		producer.mu.Lock()
		defer producer.mu.Unlock()
		producer.closed = true
		if producer.owned {
			producer.closeErr = producer.client.Close()
		}
	})
	return producer.closeErr
}
