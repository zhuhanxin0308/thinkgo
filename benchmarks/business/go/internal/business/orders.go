package business

import (
	"context"
	"database/sql"
	"errors"
)

func (service *Service) CreateOrder(ctx context.Context, principal Principal, key string, input OrderInput) (order OrderResponse, replay bool, result error) {
	if err := ValidateIdempotencyKey(key); err != nil {
		return order, false, err
	}
	items, err := ValidateOrder(input, service.config.Dataset)
	if err != nil {
		return order, false, err
	}
	tx, err := service.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return order, false, err
	}
	defer rollback(tx, &result)
	err = tx.QueryRowContext(ctx, service.queries["reserve_order"], principal.TenantID, principal.UserID, key).Scan(&order.OrderID)
	if errors.Is(err, sql.ErrNoRows) {
		order, err = service.existingOrder(ctx, tx, principal, key, items)
		if err != nil {
			return order, true, err
		}
		replay = true
	} else if err != nil {
		return order, false, err
	} else {
		order.Items = make([]OrderItem, 0, len(items))
		order.Status = "created"
		for _, item := range items {
			created, err := service.orderItem(ctx, tx, principal, order.OrderID, item)
			if err != nil {
				return order, false, err
			}
			order.Items = append(order.Items, created)
			order.TotalCents += created.Quantity * created.UnitPriceCents
		}
		if _, err := tx.ExecContext(ctx, service.queries["finish_order"], order.TotalCents, order.OrderID); err != nil {
			return order, false, err
		}
		if _, err := tx.ExecContext(ctx, service.queries["order_audit"], order.OrderID, principal.UserID, order.TotalCents); err != nil {
			return order, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return order, replay, err
	}
	// 库存以事务中的条件更新为准；详情缓存是展示快照，提交后显式失效。
	keys := make([]string, len(order.Items))
	for index, item := range order.Items {
		keys[index] = service.cacheKey(principal, item.ProductID)
	}
	if err := service.cache.Del(ctx, keys...).Err(); err != nil {
		return order, replay, err
	}
	return order, replay, nil
}

func (service *Service) orderItem(ctx context.Context, tx *sql.Tx, principal Principal, orderID int64, input OrderItemInput) (OrderItem, error) {
	item := OrderItem{ProductID: input.ProductID, Quantity: input.Quantity}
	var stock int64
	err := tx.QueryRowContext(ctx, service.queries["lock_product"], principal.TenantID, input.ProductID).Scan(&item.UnitPriceCents, &stock)
	if errors.Is(err, sql.ErrNoRows) {
		return item, notFound()
	}
	if err != nil {
		return item, err
	}
	if stock < input.Quantity {
		return item, conflict("商品库存不足")
	}
	if item.UnitPriceCents < 1 || item.UnitPriceCents > service.config.Dataset.MaximumPriceCents {
		return item, errors.New("数据库商品价格不符合业务约束")
	}
	changed, err := tx.ExecContext(ctx, service.queries["deduct_stock"], input.Quantity, principal.TenantID, input.ProductID, input.Quantity)
	if err != nil {
		return item, err
	}
	affected, err := changed.RowsAffected()
	if err != nil {
		return item, err
	}
	if affected != 1 {
		return item, conflict("商品库存发生变化")
	}
	_, err = tx.ExecContext(ctx, service.queries["insert_order_item"], orderID, input.ProductID, input.Quantity, item.UnitPriceCents)
	return item, err
}

func (service *Service) existingOrder(ctx context.Context, tx *sql.Tx, principal Principal, key string, input []OrderItemInput) (OrderResponse, error) {
	order := OrderResponse{Items: make([]OrderItem, 0, len(input))}
	err := tx.QueryRowContext(ctx, service.queries["existing_order"], principal.TenantID, principal.UserID, key).
		Scan(&order.OrderID, &order.TotalCents, &order.Status)
	if err != nil {
		return order, err
	}
	rows, err := tx.QueryContext(ctx, service.queries["existing_order_items"], order.OrderID)
	if err != nil {
		return order, err
	}
	defer rows.Close()
	for rows.Next() {
		var item OrderItem
		if err := rows.Scan(&item.ProductID, &item.Quantity, &item.UnitPriceCents); err != nil {
			return order, err
		}
		order.Items = append(order.Items, item)
	}
	if err := rows.Err(); err != nil {
		return order, err
	}
	if order.Status != "created" || len(order.Items) != len(input) {
		return order, conflict("幂等键对应的订单内容不一致")
	}
	for index, item := range order.Items {
		if item.ProductID != input[index].ProductID || item.Quantity != input[index].Quantity {
			return order, conflict("幂等键对应的订单内容不一致")
		}
	}
	return order, nil
}
