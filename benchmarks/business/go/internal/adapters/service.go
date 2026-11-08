// Package adapters 定义业务与框架适配之间的窄接口，不引用任何被比较框架。
package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
)

const PrincipalKey = "benchmark.principal"

var jsonMediaType = regexp.MustCompile(`(?i)^[ \t]*application/json[ \t]*(?:;[ \t]*charset[ \t]*=[ \t]*(?:utf-8|"utf-8")[ \t]*)?$`)

type jsonShape uint8

const (
	documentShape jsonShape = iota
	itemsShape
	itemShape
	commentShape
	integerShape
)

// Service 让适配契约测试隔离数据库，同时正式入口使用同一个真实业务实现。
type Service interface {
	Authenticate(string) (business.Principal, error)
	Catalog(context.Context, business.Principal, url.Values) (business.CatalogResponse, error)
	Detail(context.Context, business.Principal, string) (business.DetailResponse, string, error)
	CreateOrder(context.Context, business.Principal, string, business.OrderInput) (business.OrderResponse, bool, error)
	AddComment(context.Context, business.Principal, string, business.CommentInput) (business.CommentResponse, error)
	Limits() business.Dataset
}

func Timeout(service Service) time.Duration {
	return time.Duration(service.Limits().RequestTimeoutSeconds) * time.Second
}

// Catalog 保留原始查询解码错误，避免非法参数被静默丢弃后退回默认条件。
func Catalog(service Service, ctx context.Context, principal business.Principal, raw string) (business.CatalogResponse, error) {
	values, err := parseCatalogQuery(raw)
	if err != nil {
		return business.CatalogResponse{}, err
	}
	return service.Catalog(ctx, principal, values)
}

func parseCatalogQuery(raw string) (url.Values, error) {
	if raw != "" {
		for _, pair := range strings.Split(raw, "&") {
			if !strings.Contains(pair, "=") {
				return nil, business.InvalidInput("查询参数必须采用键值对")
			}
		}
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, business.InvalidInput("查询参数编码无效")
	}
	return values, nil
}

// ValidateMedia 在各框架绑定前执行同一媒体约定，不接受隐式表单或压缩正文。
func ValidateMedia(contentType, encoding string) error {
	encoding = strings.Trim(encoding, " \t")
	if !jsonMediaType.MatchString(contentType) || (encoding != "" && !strings.EqualFold(encoding, "identity")) {
		return business.InvalidInput("请求正文必须是未压缩的JSON")
	}
	return nil
}

// ValidateObject 在一次遍历中验证业务字段白名单和成员唯一性；绑定仍由原生API完成。
func ValidateObject(data []byte, limit int64) error {
	trimmed := bytes.TrimSpace(data)
	if int64(len(data)) > limit || len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(data) || !json.Valid(data) {
		return business.InvalidInput("JSON正文无效或超过大小限制")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if !uniqueValue(decoder, documentShape) {
		return business.InvalidInput("JSON正文字段、类型或成员唯一性不符合合同")
	}
	return nil
}

func uniqueValue(decoder *json.Decoder, shape jsonShape) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	switch token {
	case json.Delim('{'):
		if shape != documentShape && shape != itemShape {
			return false
		}
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return false
			}
			key, ok := keyToken.(string)
			if !ok {
				return false
			}
			if _, exists := seen[key]; exists {
				return false
			}
			seen[key] = struct{}{}
			child := integerShape
			if shape == documentShape {
				switch key {
				case "items":
					child = itemsShape
				case "body":
					child = commentShape
				default:
					return false
				}
			} else if key != "product_id" && key != "quantity" {
				return false
			}
			if !uniqueValue(decoder, child) {
				return false
			}
		}
		if (shape == documentShape && len(seen) != 1) || (shape == itemShape && len(seen) != 2) {
			return false
		}
		_, err = decoder.Token()
	case json.Delim('['):
		if shape != itemsShape {
			return false
		}
		for decoder.More() {
			if !uniqueValue(decoder, itemShape) {
				return false
			}
		}
		_, err = decoder.Token()
	default:
		if shape == commentShape {
			_, valid := token.(string)
			return valid
		}
		if shape == integerShape {
			_, valid := token.(json.Number)
			return valid
		}
		return false
	}
	return err == nil
}

// PrepareBody 保留有限正文给框架原生绑定器，验证失败时不会触发业务调用。
func PrepareBody(request *http.Request, limit int64) error {
	if err := ValidateMedia(request.Header.Get("Content-Type"), request.Header.Get("Content-Encoding")); err != nil {
		return err
	}
	if request.Body == nil {
		return business.InvalidInput("JSON正文不能为空")
	}
	defer request.Body.Close()
	data, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil {
		return business.InvalidInput("无法读取JSON正文")
	}
	if err := ValidateObject(data, limit); err != nil {
		return err
	}
	request.Body = io.NopCloser(bytes.NewReader(data))
	return nil
}

func OrderStatus(replayed bool) int {
	if replayed {
		return http.StatusOK
	}
	return http.StatusCreated
}
