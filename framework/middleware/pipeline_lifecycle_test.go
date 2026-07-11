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

// TestPipelineTerminatorsOnlyForExecutedMiddleware 验证短路时只返回真实执行过的 terminate，
// 未进入 handle 生命周期的后续中间件不能参与请求结束回调。
func TestPipelineTerminatorsOnlyForExecutedMiddleware(t *testing.T) {
	pipeline := NewPipeline()
	order := make([]string, 0)

	pipeline.PipeLifecycle(
		func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
			order = append(order, "handle:first")
			return fwcontext.NewResponse().Content("blocked")
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
	if resp == nil || string(resp.GetBody()) != "blocked" {
		t.Fatalf("短路中间件应直接返回 blocked 响应，实际为 %#v", resp)
	}
	if len(terminators) != 1 {
		t.Fatalf("短路后只应收集已执行中间件的 terminate，实际为 %d", len(terminators))
	}

	for _, terminate := range terminators {
		terminate(req, resp)
	}

	expected := []string{
		"handle:first",
		"terminate:first",
	}
	if len(order) != len(expected) {
		t.Fatalf("短路执行顺序长度不正确，实际为 %#v", order)
	}
	for index, value := range expected {
		if order[index] != value {
			t.Fatalf("短路执行顺序不正确，期望 %#v，实际为 %#v", expected, order)
		}
	}
}
