package asynq

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	backend "github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/zhuhanxin0308/thinkgo/framework/queue"
)

// TestBorrowedProducerClosePreservesClient 验证关闭借用型生产者不会报错或关闭外部客户端。
func TestBorrowedProducerClosePreservesClient(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	producer, err := NewProducerFromRedisClient(client)
	if err != nil {
		t.Fatal(err)
	}
	task, err := queue.NewTask("lifecycle.check", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = producer.Enqueue(t.Context(), task, queue.EnqueueOptions{}); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err = producer.Close(); err != nil {
			t.Fatalf("关闭借用型生产者失败: %v", err)
		}
	}
	if err = client.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("外部客户端被生产者关闭: %v", err)
	}
	if _, err = producer.Enqueue(t.Context(), task, queue.EnqueueOptions{}); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("关闭后的生产者必须拒绝入队: %v", err)
	}
}

type blockedQueueClient struct {
	redis.UniversalClient
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
	err     error
}

func (client *blockedQueueClient) Close() error {
	if client.calls.Add(1) == 1 {
		close(client.entered)
	}
	<-client.release
	return client.err
}

// TestConcurrentShutdownWaitsAndSharesError 验证所有关闭调用都等待实际资源关闭，并返回同一错误链。
func TestConcurrentShutdownWaitsAndSharesError(t *testing.T) {
	const waitBudget = 2 * time.Second
	const pendingWindow = 50 * time.Millisecond
	for _, kind := range []string{"worker", "scheduler"} {
		t.Run(kind, func(t *testing.T) {
			closeErr := errors.New("测试客户端关闭失败")
			client := &blockedQueueClient{entered: make(chan struct{}), release: make(chan struct{}), err: closeErr}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(client.release) }) }
			t.Cleanup(release)
			var shutdown func() error
			if kind == "worker" {
				shutdown = (&Worker{server: &backend.Server{}, ownedClient: client}).Shutdown
			} else {
				shutdown = (&Scheduler{scheduler: &backend.Scheduler{}, ownedClient: client}).Shutdown
			}
			results := make(chan error, 2)
			go func() { results <- shutdown() }()
			ctx, cancel := context.WithTimeout(t.Context(), waitBudget)
			defer cancel()
			select {
			case <-client.entered:
			case <-ctx.Done():
				t.Fatal("关闭未进入受管客户端")
			}
			go func() { results <- shutdown() }()
			select {
			case err := <-results:
				t.Fatalf("资源尚未关闭时提前返回: %v", err)
			case <-time.After(pendingWindow):
			}
			release()
			for attempt := 0; attempt < 2; attempt++ {
				select {
				case err := <-results:
					if !errors.Is(err, closeErr) || !errors.Is(err, queue.ErrBackendUnavailable) {
						t.Fatalf("关闭错误未共享或丢失原因: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("资源关闭后调用仍未返回")
				}
			}
			if err := shutdown(); !errors.Is(err, closeErr) || client.calls.Load() != 1 {
				t.Fatalf("重复关闭不幂等: calls=%d err=%v", client.calls.Load(), err)
			}
		})
	}
}
