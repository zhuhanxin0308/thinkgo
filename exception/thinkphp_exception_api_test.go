package exception

import (
	"errors"
	"net/http"
	"reflect"
	"testing"
)

// TestValidateExceptionThinkPHPAPI 验证单字段和批量验证异常的 getError、
// getKey、Error 与防御性数据隔离语义。
func TestValidateExceptionThinkPHPAPI(t *testing.T) {
	single := NewValidateException("邮箱格式错误", "email")
	if single.Error() != "邮箱格式错误" || single.GetError() != "邮箱格式错误" || single.GetKey() != "email" {
		t.Fatalf("单字段验证异常 API 错误: error=%q value=%#v key=%q", single.Error(), single.GetError(), single.GetKey())
	}

	source := map[string]string{"email": "邮箱格式错误", "password": "密码长度不足"}
	batch := NewBatchValidateException(source)
	source["email"] = "changed"
	first, ok := batch.GetError().(map[string]string)
	if !ok || first["email"] != "邮箱格式错误" || first["password"] != "密码长度不足" {
		t.Fatalf("批量验证错误内容错误: %#v", batch.GetError())
	}
	first["email"] = "mutated"
	if second := batch.GetError().(map[string]string); second["email"] != "邮箱格式错误" {
		t.Fatalf("GetError 不得暴露内部错误映射: %#v", second)
	}
	if batch.Error() == "" || batch.GetKey() != "" {
		t.Fatalf("批量验证异常文本或 key 错误: error=%q key=%q", batch.Error(), batch.GetKey())
	}

	var nilException *ValidateException
	if nilException.Error() != "验证异常为空" || nilException.GetError() != nil || nilException.GetKey() != "" {
		t.Fatal("nil ValidateException 必须安全返回稳定值")
	}
}

// TestBusinessAndHTTPExceptionChainAPI 验证 ThinkPHP 风格异常的状态、data、
// cause 链与 nil 接收者均保持稳定行为。
func TestBusinessAndHTTPExceptionChainAPI(t *testing.T) {
	cause := errors.New("database unavailable")
	data := map[string]interface{}{"order_id": 7}
	business := NewBusinessException(1001, "订单不可用").
		WithStatus(http.StatusConflict).
		WithData(data).
		WithCause(cause)
	data["order_id"] = 8
	if business.StatusCode() != http.StatusConflict || business.Data["order_id"] != 7 || !errors.Is(business, cause) {
		t.Fatalf("业务异常链式 API 错误: status=%d data=%#v unwrap=%v", business.StatusCode(), business.Data, business.Unwrap())
	}
	business.WithStatus(http.StatusOK)
	if business.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("非法业务异常状态必须安全回退 500: %d", business.StatusCode())
	}

	httpData := map[string]interface{}{"resource": "user"}
	httpException := NewHttpException(http.StatusNotFound, "用户不存在").WithData(httpData)
	httpData["resource"] = "changed"
	if httpException.Error() != "HTTP 404: 用户不存在" || !reflect.DeepEqual(httpException.Data, map[string]interface{}{"resource": "user"}) {
		t.Fatalf("HTTP 异常 API 错误: text=%q data=%#v", httpException.Error(), httpException.Data)
	}

	var nilBusiness *BusinessException
	if nilBusiness.Error() != "业务异常为空" || nilBusiness.StatusCode() != http.StatusInternalServerError || nilBusiness.Unwrap() != nil {
		t.Fatal("nil BusinessException 必须安全返回稳定值")
	}
	if nilBusiness.WithStatus(http.StatusBadRequest) != nil || nilBusiness.WithData(nil) != nil || nilBusiness.WithCause(cause) != nil {
		t.Fatal("nil BusinessException 链式方法必须返回 nil")
	}
	var nilHTTP *HttpException
	if nilHTTP.Error() != "HTTP 异常为空" || nilHTTP.WithData(nil) != nil {
		t.Fatal("nil HttpException 必须安全返回稳定值")
	}
}
