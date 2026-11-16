package business

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestServiceOrderOverflowRollsBack(t *testing.T) {
	service := integrationService(t)
	service.config.Dataset.MaximumPriceCents = math.MaxInt64
	ctx := t.Context()
	principal := Principal{TenantID: 1, UserID: 1}
	// 单独创建回归商品，避免改变共享种子数据。
	const smallID, largeID, stock = math.MaxInt64 - 1, math.MaxInt64, 4
	if _, err := service.database.ExecContext(ctx, `
		INSERT INTO bench_products (id, tenant_id, category_id, name, description, price_cents, stock, active, internal_cost_cents)
		VALUES ($1, 1, 1, 'overflow fixture', '', 2, $3, TRUE, 0),
		       ($2, 1, 1, 'overflow fixture', '', $2, $3, TRUE, 0)`, smallID, largeID, stock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := service.database.Exec("DELETE FROM bench_products WHERE id IN ($1, $2)", smallID, largeID); err != nil {
			t.Error(err)
		}
	})
	for _, test := range []struct {
		name  string
		items []OrderItemInput
		total int64
	}{
		{name: "乘积回绕", items: []OrderItemInput{{ProductID: largeID, Quantity: 3}}},
		{name: "累计溢出", items: []OrderItemInput{{ProductID: smallID, Quantity: 1}, {ProductID: largeID, Quantity: 1}}},
		{name: "接受最大合法金额", items: []OrderItemInput{{ProductID: largeID, Quantity: 1}}, total: math.MaxInt64},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := fmt.Sprintf("%032x", time.Now().UnixNano())
			t.Cleanup(func() {
				tx, err := service.database.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				for _, query := range []string{
					"DELETE FROM bench_order_audit WHERE order_id IN (SELECT id FROM bench_orders WHERE idempotency_key = $1)",
					"DELETE FROM bench_order_items WHERE order_id IN (SELECT id FROM bench_orders WHERE idempotency_key = $1)",
					"DELETE FROM bench_orders WHERE idempotency_key = $1",
				} {
					if _, err := tx.Exec(query, key); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := tx.Exec("UPDATE bench_products SET stock = $1 WHERE id IN ($2, $3)", stock, smallID, largeID); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			})
			order, _, err := service.CreateOrder(t.Context(), principal, key, OrderInput{Items: test.items})
			if test.total > 0 {
				if err != nil || order.TotalCents != test.total {
					t.Fatalf("合法订单金额 = %d，错误 %v", order.TotalCents, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "订单金额超出整数范围") {
				t.Errorf("预期订单金额溢出，得到 %v", err)
			}
			var orders int
			if err := service.database.QueryRowContext(t.Context(),
				"SELECT count(*) FROM bench_orders WHERE idempotency_key = $1", key).Scan(&orders); err != nil || orders != 0 {
				t.Errorf("失败订单残留 = %d，错误 %v", orders, err)
			}
			for _, id := range []int64{smallID, largeID} {
				var remaining int64
				if err := service.database.QueryRowContext(t.Context(),
					"SELECT stock FROM bench_products WHERE id = $1", id).Scan(&remaining); err != nil || remaining != stock {
					t.Errorf("商品 %d 库存 = %d，错误 %v", id, remaining, err)
				}
			}
		})
	}
}
