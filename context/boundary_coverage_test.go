package context

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestResponseNilAndInvalidStreamBoundaries 验证空响应和空流回调不会绕过错误状态。
func TestResponseNilAndInvalidStreamBoundaries(t *testing.T) {
	var nilResponse *Response
	if !errors.Is(nilResponse.Error(), ErrInvalidResponseWriter) {
		t.Fatalf("空响应 Error 应返回响应错误: %v", nilResponse.Error())
	}
	recorder := httptest.NewRecorder()
	response := NewResponse().Stream(nil)
	if !errors.Is(response.Error(), ErrInvalidStream) {
		t.Fatalf("空流回调应记录 ErrInvalidStream: %v", response.Error())
	}
	if err := response.Send(recorder); !errors.Is(err, ErrInvalidStream) {
		t.Fatalf("发送空流响应应返回 ErrInvalidStream: %v", err)
	}
	if recorder.Code != http.StatusInternalServerError || recorder.Body.Len() == 0 {
		t.Fatalf("空流响应应安全降级为通用 500: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

// TestZeroResponseAcceptsValidStream 验证零值响应也能安全配置有效流回调。
func TestZeroResponseAcceptsValidStream(t *testing.T) {
	var zero Response
	if zero.Stream(func(io.Writer) error { return nil }).Error() != nil {
		t.Fatal("零值 Response 设置有效流回调不应因空 Header 失败")
	}
}

type panicResponseError struct{}

func (panicResponseError) Error() string {
	panic("响应传输错误不应泄露底层 panic")
}

// TestResponseTransmissionErrorSafety 验证传输错误包装器在空值和异常 Error 实现下仍保持安全文本。
func TestResponseTransmissionErrorSafety(t *testing.T) {
	var nilTransmission *responseTransmissionError
	if nilTransmission.Error() != "响应传输失败" || nilTransmission.Unwrap() != nil {
		t.Fatalf("空传输错误包装器结果错误: error=%q unwrap=%v", nilTransmission.Error(), nilTransmission.Unwrap())
	}
	wrapped := &responseTransmissionError{err: panicResponseError{}}
	if wrapped.Error() != "响应传输失败" {
		t.Fatalf("底层 Error panic 时应返回通用错误: %q", wrapped.Error())
	}
	underlying := errors.New("transport failure")
	wrapped = &responseTransmissionError{err: underlying}
	if wrapped.Error() != underlying.Error() || !errors.Is(wrapped, underlying) {
		t.Fatalf("传输错误应保留可诊断的 Unwrap: error=%q unwrap=%v", wrapped.Error(), wrapped.Unwrap())
	}
}
