package loadrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
)

var (
	errResponseStatus  = errors.New("response_status")
	errResponseJSON    = errors.New("response_json_contract")
	errResponseMedia   = errors.New("response_content_type")
	errResponseData    = errors.New("response_business_contract")
	errResponseCache   = errors.New("response_cache_header")
	errResponseTooLong = errors.New("response_body_limit")
)

type productCard struct {
	ID         *int64  `json:"id"`
	Name       *string `json:"name"`
	PriceCents *int64  `json:"price_cents"`
	Category   *string `json:"category"`
	Stock      *int64  `json:"stock"`
}

type catalogCursor struct {
	PriceCents *int64 `json:"price_cents"`
	ID         *int64 `json:"id"`
}

type catalogResponse struct {
	Items      []productCard   `json:"items"`
	NextCursor json.RawMessage `json:"next_cursor"`
}

type productSpec struct {
	Name  *string `json:"name"`
	Value *string `json:"value"`
}

type detailResponse struct {
	productCard
	Description *string       `json:"description"`
	Specs       []productSpec `json:"specs"`
}

type orderOutputItem struct {
	ProductID      *int64 `json:"product_id"`
	Quantity       *int64 `json:"quantity"`
	UnitPriceCents *int64 `json:"unit_price_cents"`
}

type orderResponse struct {
	OrderID    *int64            `json:"order_id"`
	TotalCents *int64            `json:"total_cents"`
	Status     *string           `json:"status"`
	Items      []orderOutputItem `json:"items"`
}

type ticketResponse struct {
	CommentID *int64  `json:"comment_id"`
	TicketID  *int64  `json:"ticket_id"`
	Version   *int64  `json:"version"`
	Status    *string `json:"status"`
}

func expectedStatus(scenario string) int {
	if scenario == "order" || scenario == "ticket" {
		return http.StatusCreated
	}
	return http.StatusOK
}

// validateResponse 每次请求都验证业务结果，错误只返回固定分类，不保存服务端正文。
func validateResponse(configuration Config, request requestSpec, status int, headers http.Header, body []byte) error {
	if status != expectedStatus(request.Scenario) {
		return errResponseStatus
	}
	mediaType, _, err := mime.ParseMediaType(headers.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errResponseMedia
	}
	switch request.Scenario {
	case "catalog":
		return validateCatalog(configuration, request, body)
	case "detail":
		cache := headers.Get("X-Benchmark-Cache")
		if cache != "hit" && cache != "miss" {
			return errResponseCache
		}
		return validateDetail(configuration, request, body)
	case "order":
		return validateOrder(request, body)
	case "ticket":
		return validateTicket(request, body)
	default:
		return errResponseData
	}
}

func decodeStrict(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errResponseJSON
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errResponseJSON
	}
	return nil
}

func validateCard(configuration Config, tenant int, card productCard) bool {
	if card.ID == nil || card.PriceCents == nil || card.Stock == nil || card.Name == nil || card.Category == nil {
		return false
	}
	local := *card.ID - int64(tenant-1)*int64(configuration.ProductsPerTenant)
	return local > 0 && local <= int64(configuration.ProductsPerTenant) && local%inactiveProductEvery != 0 &&
		*card.PriceCents > 0 && *card.Stock >= 0 && *card.Name != "" && *card.Category != ""
}

func validateCatalog(configuration Config, request requestSpec, body []byte) error {
	var response catalogResponse
	if err := decodeStrict(body, &response); err != nil {
		return err
	}
	if len(response.Items) == 0 || len(response.Items) > catalogPageSize || response.NextCursor == nil {
		return errResponseData
	}
	for index, card := range response.Items {
		if !validateCard(configuration, request.Tenant, card) || *card.PriceCents > catalogMaximumPrice {
			return errResponseData
		}
		if index > 0 {
			previous := response.Items[index-1]
			if *card.PriceCents < *previous.PriceCents || (*card.PriceCents == *previous.PriceCents && *card.ID <= *previous.ID) {
				return errResponseData
			}
		}
	}
	if !bytes.Equal(bytes.TrimSpace(response.NextCursor), []byte("null")) {
		var cursor catalogCursor
		if err := decodeStrict(response.NextCursor, &cursor); err != nil {
			return err
		}
		last := response.Items[len(response.Items)-1]
		if cursor.ID == nil || cursor.PriceCents == nil || *cursor.ID != *last.ID || *cursor.PriceCents != *last.PriceCents {
			return errResponseData
		}
	}
	return nil
}

func validateDetail(configuration Config, request requestSpec, body []byte) error {
	var response detailResponse
	if err := decodeStrict(body, &response); err != nil {
		return err
	}
	if !validateCard(configuration, request.Tenant, response.productCard) || *response.ID != request.ResourceID || response.Description == nil || *response.Description == "" || len(response.Specs) == 0 {
		return errResponseData
	}
	for _, specification := range response.Specs {
		if specification.Name == nil || specification.Value == nil || *specification.Name == "" || *specification.Value == "" {
			return errResponseData
		}
	}
	return nil
}

func validateOrder(request requestSpec, body []byte) error {
	var response orderResponse
	if err := decodeStrict(body, &response); err != nil {
		return err
	}
	if response.OrderID == nil || *response.OrderID <= 0 || response.TotalCents == nil || response.Status == nil || *response.Status != "created" || len(response.Items) != len(request.OrderItems) || len(response.Items) == 0 {
		return errResponseData
	}
	expected := make(map[int64]int64, len(request.OrderItems))
	for _, item := range request.OrderItems {
		expected[item.ProductID] = item.Quantity
	}
	var total int64
	for _, item := range response.Items {
		if item.ProductID == nil || item.Quantity == nil || item.UnitPriceCents == nil || *item.Quantity < 1 || *item.UnitPriceCents <= 0 || expected[*item.ProductID] != *item.Quantity {
			return errResponseData
		}
		if *item.UnitPriceCents > math.MaxInt64/(*item.Quantity) {
			return errResponseData
		}
		line := *item.Quantity * *item.UnitPriceCents
		if total > math.MaxInt64-line {
			return errResponseData
		}
		total += line
		delete(expected, *item.ProductID)
	}
	if len(expected) != 0 || *response.TotalCents != total {
		return errResponseData
	}
	return nil
}

func validateTicket(request requestSpec, body []byte) error {
	var response ticketResponse
	if err := decodeStrict(body, &response); err != nil {
		return err
	}
	if response.CommentID == nil || *response.CommentID <= 0 || response.TicketID == nil || *response.TicketID != request.ResourceID || response.Version == nil || *response.Version <= 0 || response.Status == nil || *response.Status != "open" {
		return errResponseData
	}
	return nil
}
