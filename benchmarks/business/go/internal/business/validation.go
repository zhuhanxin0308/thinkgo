package business

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const idempotencyKeyLength = 32

func ParseCatalog(values url.Values, limits Dataset) (CatalogQuery, error) {
	query := CatalogQuery{CategoryID: 1, MaximumPrice: limits.MaximumPriceCents, Limit: limits.CatalogPageSize}
	if values.Has("cursor_price") != values.Has("cursor_id") {
		return query, InvalidInput("游标参数必须同时提供")
	}
	allowed := map[string]*int64{
		"category_id": &query.CategoryID, "min_price": &query.MinimumPrice,
		"max_price": &query.MaximumPrice, "cursor_price": &query.CursorPrice, "cursor_id": &query.CursorID,
	}
	for name, entries := range values {
		if len(entries) != 1 {
			return query, InvalidInput("查询参数不能重复")
		}
		parsed, err := decimal(entries[0])
		if err != nil {
			return query, err
		}
		if name == "limit" {
			if parsed < 1 || parsed > int64(limits.MaximumPageSize) {
				return query, InvalidInput("分页大小超出范围")
			}
			query.Limit = int(parsed)
		} else if destination, exists := allowed[name]; exists {
			*destination = parsed
		} else {
			return query, InvalidInput("包含未知查询参数")
		}
	}
	if query.CategoryID < 1 || query.CategoryID > limits.CategoriesPerTenant ||
		query.MinimumPrice > query.MaximumPrice || query.MaximumPrice > limits.MaximumPriceCents ||
		query.CursorPrice > limits.MaximumPriceCents ||
		(query.CursorPrice == 0) != (query.CursorID == 0) {
		return query, InvalidInput("分类、价格或游标超出范围")
	}
	return query, nil
}

func decimal(value string) (int64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, InvalidInput("整数参数不能为空或带有前导零")
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, InvalidInput("参数必须是非负十进制整数")
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, InvalidInput("整数参数超出范围")
	}
	return parsed, nil
}

func recordID(value string) (int64, error) {
	id, err := decimal(value)
	if err == nil && id == 0 {
		err = InvalidInput("记录编号必须为正整数")
	}
	return id, err
}

func ValidateOrder(input OrderInput, limits Dataset) ([]OrderItemInput, error) {
	if len(input.Items) < 1 || len(input.Items) > limits.MaximumOrderItems {
		return nil, InvalidInput("订单商品数量超出范围")
	}
	items := slices.Clone(input.Items)
	slices.SortFunc(items, func(left, right OrderItemInput) int {
		if left.ProductID < right.ProductID {
			return -1
		}
		if left.ProductID > right.ProductID {
			return 1
		}
		return 0
	})
	for index, item := range items {
		if item.ProductID < 1 || item.Quantity < 1 || item.Quantity > limits.MaximumItemQuantity ||
			(index > 0 && items[index-1].ProductID == item.ProductID) {
			return nil, InvalidInput("商品、数量或重复商品不合法")
		}
	}
	return items, nil
}

func ValidateIdempotencyKey(key string) error {
	if len(key) != idempotencyKeyLength {
		return InvalidInput("幂等键必须是32位小写十六进制字符串")
	}
	for _, char := range key {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return InvalidInput("幂等键必须是32位小写十六进制字符串")
		}
	}
	return nil
}

func ValidateComment(input CommentInput, limits Dataset) (string, error) {
	if !utf8.ValidString(input.Body) {
		return "", InvalidInput("评论必须采用合法UTF-8编码")
	}
	body := strings.TrimSpace(input.Body)
	length := utf8.RuneCountInString(body)
	if length < limits.MinimumCommentCharacters || length > limits.MaximumCommentCharacters {
		return "", InvalidInput("评论字符数超出范围")
	}
	return body, nil
}
