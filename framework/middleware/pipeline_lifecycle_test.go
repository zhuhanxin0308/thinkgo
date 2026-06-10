package middleware

import (
	"testing"

	fwcontext "thinkgo/framework/context"
)

func TestPipelineTerminateLifecycle(t *testing.T) {
	pipeline := NewPipeline()
	order := make([]string, 0)

	pipeline.PipeLifecycle(
		func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			order = append(order, "handle:first")
			return next(req)
		},
		func(req *fwcontext.Request, resp *fwcontext.Response) {
			order = append(order, "terminate:first")
		},
	)
	pipeline.PipeLifecycle(
		func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			order = append(order, "handle:second")
			return next(req)
		},
		func(req *fwcontext.Request, resp *fwcontext.Response) {
			order = append(order, "terminate:second")
		},
	)

	req := fwcontext.NewRequest(nil)
	resp, terminators := pipeline.ThenWithTerminators(req, func(req *fwcontext.Request) *fwcontext.Response {
		order = append(order, "destination")
		return fwcontext.NewResponse().Content("ok")
	})
	if resp == nil || string(resp.GetBody()) != "ok" {
		t.Fatalf("ThenWithTerminators 应返回目标响应，实际为 %#v", resp)
	}
	if len(terminators) != 2 {
		t.Fatalf("应收集 2 个 terminate 回调，实际为 %d", len(terminators))
	}

	for _, terminate := range terminators {
		terminate(req, resp)
	}

	expected := []string{
		"handle:first",
		"handle:second",
		"destination",
		"terminate:first",
		"terminate:second",
	}
	if len(order) != len(expected) {
		t.Fatalf("执行顺序长度不正确，实际为 %#v", order)
	}
	for index, value := range expected {
		if order[index] != value {
			t.Fatalf("执行顺序不正确，期望 %#v，实际为 %#v", expected, order)
		}
	}
}
