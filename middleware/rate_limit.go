package middleware

import (
	"errors"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/ratelimit"
)

// RateLimitKey 从已经完成可信代理解析的请求中生成稳定键。
type RateLimitKey func(*context.Request) (string, error)

// RateLimitConfig 组合原子存储、速率策略和键策略。
type RateLimitConfig struct {
	Store ratelimit.Store
	Limit ratelimit.Limit
	Key   RateLimitKey
}

// RateLimit 是同时支持框架响应和 net/http 处理器的限流中间件。
type RateLimit struct {
	store ratelimit.Store
	limit ratelimit.Limit
	key   RateLimitKey
	clock func() time.Time
}

// NewRateLimit 严格校验依赖；默认键使用 Request.Ip 的受信代理解析结果。
func NewRateLimit(config RateLimitConfig) (*RateLimit, error) {
	if isNilRateLimitStore(config.Store) {
		return nil, ratelimit.ErrInvalidConfiguration
	}
	if _, err := ratelimit.Validate(config.Limit); err != nil {
		return nil, err
	}
	if config.Key == nil {
		config.Key = ClientIPRateLimitKey
	}
	return &RateLimit{store: config.Store, limit: config.Limit, key: config.Key, clock: time.Now}, nil
}

// ClientIPRateLimitKey 返回标准化 IP，默认不会直接信任客户端提供的转发头。
func ClientIPRateLimitKey(request *context.Request) (string, error) {
	if request == nil {
		return "", ratelimit.ErrInvalidKey
	}
	ip := net.ParseIP(strings.TrimSpace(request.Ip()))
	if ip == nil {
		return "", ratelimit.ErrInvalidKey
	}
	return ip.String(), nil
}

// Handle 原子消费额度，并在所有响应路径写出标准限流元数据。
func (middleware *RateLimit) Handle(request *context.Request, next func(*context.Request) *context.Response) *context.Response {
	if middleware == nil || request == nil || next == nil || middleware.clock == nil {
		return rateLimitErrorResponse(http.StatusInternalServerError)
	}
	key, err := middleware.key(request)
	if err != nil || strings.TrimSpace(key) == "" {
		return rateLimitErrorResponse(http.StatusBadRequest)
	}
	result, err := middleware.store.Take(request.Context(), key, middleware.limit, middleware.clock())
	if err != nil {
		if errors.Is(err, ratelimit.ErrStoreCapacity) {
			// 存储容量耗尽是服务端保护能力不可用，不代表当前客户端额度超限。
			response := rateLimitErrorResponse(http.StatusServiceUnavailable)
			response.Header("Retry-After", "1")
			return response
		}
		return rateLimitErrorResponse(http.StatusServiceUnavailable)
	}
	headers := rateLimitHeaders(result)
	if !result.Allowed {
		headers.Set("Retry-After", durationSeconds(result.RetryAfter))
		headers.Set("Cache-Control", "no-store")
		response := context.NewResponse().Code(http.StatusTooManyRequests).Content(http.StatusText(http.StatusTooManyRequests))
		applyRateLimitResponseHeaders(response, headers)
		return response
	}
	if writer, exists := request.ResponseWriter(); exists {
		applyRateLimitHTTPHeaders(writer.Header(), headers)
	}
	response := next(request)
	if response == nil {
		response = rateLimitErrorResponse(http.StatusInternalServerError)
	}
	applyRateLimitResponseHeaders(response, headers)
	return response
}

func rateLimitHeaders(result ratelimit.Result) http.Header {
	headers := make(http.Header)
	headers.Set("RateLimit-Limit", strconv.Itoa(result.Limit))
	headers.Set("RateLimit-Remaining", strconv.Itoa(result.Remaining))
	headers.Set("RateLimit-Reset", durationSeconds(result.ResetAfter))
	return headers
}

func durationSeconds(duration time.Duration) string {
	if duration <= 0 {
		return "0"
	}
	seconds := duration / time.Second
	if duration%time.Second != 0 {
		seconds++
	}
	return strconv.FormatInt(int64(seconds), 10)
}

func applyRateLimitHTTPHeaders(target, values http.Header) {
	for name, items := range values {
		target[name] = append([]string(nil), items...)
	}
}

func applyRateLimitResponseHeaders(response *context.Response, values http.Header) {
	for name, items := range values {
		if len(items) > 0 {
			response.Header(name, items[0])
		}
	}
}

func rateLimitErrorResponse(status int) *context.Response {
	return context.NewResponse().Header("Cache-Control", "no-store").Code(status).Content(http.StatusText(status))
}

func isNilRateLimitStore(store ratelimit.Store) bool {
	if store == nil {
		return true
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
