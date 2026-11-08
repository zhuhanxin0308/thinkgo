package business

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// integrationService 只连接运行器显式创建的独立基准数据库。
func integrationService(t *testing.T) *Service {
	t.Helper()
	if os.Getenv("BENCH_INTEGRATION") != "1" {
		t.Skip("真实PostgreSQL/Redis测试需要由基准运行器统一启动依赖")
	}
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.CacheNamespace = fmt.Sprintf("bench:unit:%d", time.Now().UnixNano())
	service, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})
	return service
}

func requireStatus(t *testing.T, err error, expected int) {
	t.Helper()
	actual, _ := ErrorResult(err)
	if err == nil || actual != expected {
		t.Fatalf("预期错误%d，实际%d %v", expected, actual, err)
	}
}

func TestServiceCatalogAndRealCache(t *testing.T) {
	service := integrationService(t)
	ctx := context.Background()
	principal := Principal{TenantID: 1, UserID: 1}
	first, err := service.Catalog(ctx, principal, url.Values{"category_id": {"1"}, "limit": {"2"}})
	if err != nil || len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("第一页错误: %+v %v", first, err)
	}
	next, err := service.Catalog(ctx, principal, url.Values{
		"category_id": {"1"}, "limit": {"2"},
		"cursor_price": {fmt.Sprint(first.NextCursor.PriceCents)}, "cursor_id": {fmt.Sprint(first.NextCursor.ID)},
	})
	if err != nil || len(next.Items) != 2 || next.Items[0].ID == first.Items[1].ID {
		t.Fatalf("游标分页重复或失败: %+v %v", next, err)
	}
	detail, cache, err := service.Detail(ctx, principal, "1")
	if err != nil || cache != "miss" || len(detail.Specs) != service.Limits().SpecificationsPerProduct {
		t.Fatalf("首次读取没有真实回源: %+v %s %v", detail, cache, err)
	}
	cached, cache, err := service.Detail(ctx, principal, "1")
	if err != nil || cache != "hit" || cached.ID != detail.ID || cached.PriceCents != detail.PriceCents {
		t.Fatalf("真实Redis命中错误: %+v %s %v", cached, cache, err)
	}
	_, _, err = service.Detail(ctx, principal, "25001")
	requireStatus(t, err, 404)
	_, _, err = service.Detail(ctx, principal, "10")
	requireStatus(t, err, 404)
	_, _, err = service.Detail(ctx, principal, "0")
	requireStatus(t, err, 422)
	_, err = service.Catalog(ctx, principal, url.Values{"limit": {"0"}})
	requireStatus(t, err, 422)
	if strings.Contains(stringJSON(t, detail), "internal_cost") {
		t.Fatal("详情泄露内部成本")
	}
}

func TestServiceOrderIdempotencyRollbackAndInventory(t *testing.T) {
	service := integrationService(t)
	ctx := context.Background()
	principal := Principal{TenantID: 1, UserID: 2}
	key := fmt.Sprintf("%032x", time.Now().UnixNano())
	input := OrderInput{Items: []OrderItemInput{{ProductID: 2, Quantity: 2}, {ProductID: 3, Quantity: 1}}}
	before, _, err := service.Detail(ctx, principal, "2")
	if err != nil {
		t.Fatal(err)
	}
	order, replay, err := service.CreateOrder(ctx, principal, key, input)
	if err != nil || replay || order.OrderID <= 0 || len(order.Items) != 2 {
		t.Fatalf("订单提交失败: %+v replay=%v err=%v", order, replay, err)
	}
	var total int64
	for _, item := range order.Items {
		total += item.Quantity * item.UnitPriceCents
	}
	if total != order.TotalCents {
		t.Fatal("订单总额与明细不一致")
	}
	after, state, err := service.Detail(ctx, principal, "2")
	if err != nil || after.Stock != before.Stock-2 || state != "miss" {
		t.Fatalf("库存扣减或缓存失效错误: %+v %s %v", after, state, err)
	}
	repeated, replay, err := service.CreateOrder(ctx, principal, key, input)
	if err != nil || !replay || repeated.OrderID != order.OrderID {
		t.Fatalf("幂等重放错误: %+v %v %v", repeated, replay, err)
	}
	changed := OrderInput{Items: []OrderItemInput{{ProductID: 2, Quantity: 1}}}
	_, _, err = service.CreateOrder(ctx, principal, key, changed)
	requireStatus(t, err, 409)
	bad := OrderInput{Items: []OrderItemInput{{ProductID: 2, Quantity: 1}, {ProductID: 25001, Quantity: 1}}}
	_, _, err = service.CreateOrder(ctx, principal, fmt.Sprintf("%032x", time.Now().UnixNano()), bad)
	requireStatus(t, err, 404)
	var stock int64
	if err := service.database.QueryRowContext(ctx, "SELECT stock FROM bench_products WHERE id=2").Scan(&stock); err != nil || stock != after.Stock {
		t.Fatalf("后续商品失败时未回滚先前扣减: stock=%d err=%v", stock, err)
	}
	_, _, err = service.CreateOrder(ctx, principal, "bad", input)
	requireStatus(t, err, 422)
}

func TestServiceConcurrentIdempotencyHasOneEffect(t *testing.T) {
	service := integrationService(t)
	principal := Principal{TenantID: 1, UserID: 3}
	key := fmt.Sprintf("%032x", time.Now().UnixNano())
	input := OrderInput{Items: []OrderItemInput{{ProductID: 5, Quantity: 1}}}
	const attempts = 8
	ids := make(chan int64, attempts)
	errors := make(chan error, attempts)
	var workers sync.WaitGroup
	for range attempts {
		workers.Go(func() {
			order, _, err := service.CreateOrder(context.Background(), principal, key, input)
			ids <- order.OrderID
			errors <- err
		})
	}
	workers.Wait()
	close(ids)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var expected int64
	for id := range ids {
		if expected == 0 {
			expected = id
		} else if id != expected {
			t.Fatal("相同幂等键创建了不同订单")
		}
	}
}

func TestServiceTicketOwnershipAndCancellation(t *testing.T) {
	service := integrationService(t)
	input := CommentInput{Body: strings.Repeat("业务处理记录", 8)}
	principal := Principal{TenantID: 1, UserID: 1}
	created, err := service.AddComment(context.Background(), principal, "1", input)
	if err != nil || created.CommentID <= 0 || created.Version <= int64(service.Limits().InitialCommentsPerTicket) {
		t.Fatalf("工单评论失败: %+v %v", created, err)
	}
	_, err = service.AddComment(context.Background(), principal, "2", input)
	requireStatus(t, err, 403)
	_, err = service.AddComment(context.Background(), principal, "5001", input)
	requireStatus(t, err, 404)
	_, err = service.AddComment(context.Background(), principal, "1", CommentInput{})
	requireStatus(t, err, 422)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Catalog(ctx, principal, nil); err == nil {
		t.Fatal("已取消请求仍然执行数据库查询")
	}
}
