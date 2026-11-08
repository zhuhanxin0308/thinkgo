package serve

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
)

// 中文注释：非法公网监听配置必须在连接数据库、创建框架应用前拒绝。
func TestExecuteRejectsNonLoopbackBeforeOpeningDependencies(t *testing.T) {
	t.Setenv("BENCH_ADDR", "0.0.0.0:18080")
	t.Setenv("BENCH_REDIS_ADDR", "127.0.0.1:6379")
	factoryCalled := false
	err := execute(context.Background(), func(business.Config, *business.Service) (Application, error) {
		factoryCalled = true
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "回环TCP") {
		t.Fatalf("非法监听地址必须提前拒绝: %v", err)
	}
	if factoryCalled {
		t.Fatal("配置不可信时不得创建应用")
	}
}

func TestRunOwnedTreatsCancellationAsGracefulAndReleasesBothOwners(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var closed []string
	app := applicationProbe{closingProbe: closingProbe{&closed, "application", nil}, err: context.Canceled}
	err := RunOwned(ctx, app, closingProbe{&closed, "dependencies", nil})
	if err != nil {
		t.Fatalf("主动取消不应被记录为业务故障: %v", err)
	}
	if len(closed) != 2 || closed[0] != "application" || closed[1] != "dependencies" {
		t.Fatalf("取消后必须按资源归属反向释放: %v", closed)
	}
}

// 中文注释：监听器先被关闭时不应把非正常 Accept 失败误认为优雅停止。
func TestServeListenerPropagatesUnexpectedAcceptFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if err := serveListener(context.Background(), listener, handler, time.Second); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("异常关闭的监听器必须保留原始错误: %v", err)
	}
}

// 中文注释：真实服务资源测试只在 CI 提供隔离 PostgreSQL/Redis 时运行，
// 不自动创建或触碰开发者的数据库与缓存实例。
func TestExecuteStartsSharedBusinessServiceAndClosesApplication(t *testing.T) {
	if os.Getenv("BENCH_INTEGRATION") != "1" {
		t.Skip("需要隔离业务数据库与缓存")
	}
	var closed []string
	created := false
	err := execute(context.Background(), func(config business.Config, service *business.Service) (Application, error) {
		created = true
		if config.CacheNamespace == "" || service.Limits().SchemaVersion != 1 {
			t.Fatalf("统一业务合同未通过服务实例传递")
		}
		return applicationProbe{closingProbe: closingProbe{&closed, "application", nil}}, nil
	})
	if err != nil {
		t.Fatalf("真实数据库与缓存上的服务启动/关闭失败: %v", err)
	}
	if !created || len(closed) != 1 || closed[0] != "application" {
		t.Fatalf("应用创建或关闭遗漏: created=%v closed=%v", created, closed)
	}
}

func TestExecuteClosesSharedDependenciesWhenFactoryRejectsApplication(t *testing.T) {
	if os.Getenv("BENCH_INTEGRATION") != "1" {
		t.Skip("需要隔离业务数据库与缓存")
	}
	rejected := errors.New("模拟框架启动拒绝")
	created := false
	err := execute(context.Background(), func(_ business.Config, service *business.Service) (Application, error) {
		created = true
		if service.Limits().SchemaVersion != 1 {
			t.Fatal("框架装配失败前应完成共享业务合同加载")
		}
		return nil, rejected
	})
	if !created || !errors.Is(err, rejected) {
		t.Fatalf("框架拒绝创建时必须保留错误并释放依赖: created=%v err=%v", created, err)
	}
}
