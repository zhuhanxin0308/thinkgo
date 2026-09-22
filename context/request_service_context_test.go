package context

import (
	stdcontext "context"
	"net/http/httptest"
	"testing"
)

type contextualRequestServiceProbe struct {
	requestServiceScopeProbe
	context stdcontext.Context
}

func (probe *contextualRequestServiceProbe) MakeContext(ctx stdcontext.Context, name string, params ...interface{}) (interface{}, error) {
	probe.context = ctx
	return probe.Make(name, params...)
}

// TestRequestMakePassesLatestContext 验证可选上下文解析契约收到调用时的请求上下文。
func TestRequestMakePassesLatestContext(t *testing.T) {
	probe := &contextualRequestServiceProbe{requestServiceScopeProbe: requestServiceScopeProbe{values: map[string]interface{}{"service": "ok"}}}
	request := MustNewRequest(httptest.NewRequest("GET", "/", nil), WithServiceScope(probe, probe))
	t.Cleanup(func() { _ = request.Cleanup() })
	ctx, cancel := stdcontext.WithCancel(request.Context())
	defer cancel()
	*request.Raw() = *request.Raw().WithContext(ctx)
	value, err := request.Make("service")
	if err != nil || value != "ok" || probe.context != ctx {
		t.Fatalf("上下文未传入解析器: value=%v err=%v ctx=%v", value, err, probe.context)
	}
}
