package business

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/redis/go-redis/v9"
)

func (service *Service) Catalog(ctx context.Context, principal Principal, values url.Values) (CatalogResponse, error) {
	response := CatalogResponse{Items: make([]ProductCard, 0)}
	query, err := ParseCatalog(values, service.config.Dataset)
	if err != nil {
		return response, err
	}
	rows, err := service.database.QueryContext(ctx, service.queries["catalog"],
		principal.TenantID, query.CategoryID, query.MinimumPrice, query.MaximumPrice,
		query.CursorPrice, query.CursorID, query.Limit+1)
	if err != nil {
		return response, err
	}
	defer rows.Close()
	for rows.Next() {
		var card ProductCard
		if err := rows.Scan(&card.ID, &card.Name, &card.PriceCents, &card.Category, &card.Stock); err != nil {
			return response, err
		}
		response.Items = append(response.Items, card)
	}
	if err := rows.Err(); err != nil {
		return response, err
	}
	if len(response.Items) > query.Limit {
		response.Items = response.Items[:query.Limit]
		last := response.Items[len(response.Items)-1]
		response.NextCursor = &Cursor{PriceCents: last.PriceCents, ID: last.ID}
	}
	return response, nil
}

func (service *Service) Detail(ctx context.Context, principal Principal, id string) (DetailResponse, string, error) {
	var detail DetailResponse
	productID, err := recordID(id)
	if err != nil {
		return detail, "", err
	}
	key := service.cacheKey(principal, productID)
	cached, err := service.cache.Get(ctx, key).Bytes()
	if err == nil {
		if err := json.Unmarshal(cached, &detail); err != nil {
			return detail, "", err
		}
		return detail, "hit", nil
	}
	if !errors.Is(err, redis.Nil) {
		return detail, "", err
	}
	err = service.database.QueryRowContext(ctx, service.queries["product"], principal.TenantID, productID).
		Scan(&detail.ID, &detail.Name, &detail.PriceCents, &detail.Stock, &detail.Category, &detail.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return detail, "", notFound()
	}
	if err != nil {
		return detail, "", err
	}
	detail.Specs = make([]Specification, 0, service.config.Dataset.SpecificationsPerProduct)
	rows, err := service.database.QueryContext(ctx, service.queries["specifications"], productID, service.config.Dataset.SpecificationsPerProduct)
	if err != nil {
		return detail, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var specification Specification
		if err := rows.Scan(&specification.Name, &specification.Value); err != nil {
			return detail, "", err
		}
		detail.Specs = append(detail.Specs, specification)
	}
	if err := rows.Err(); err != nil {
		return detail, "", err
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return detail, "", err
	}
	if err := service.cache.SetEx(ctx, key, encoded, time.Duration(service.config.Dataset.CacheTTLSeconds)*time.Second).Err(); err != nil {
		return detail, "", err
	}
	return detail, "miss", nil
}
