package middleware

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func TestPipelineOuterPreservesOrdinaryPriorityAndLaterRegistration(t *testing.T) {
	for _, withPriority := range []bool{false, true} {
		pipeline := NewPipeline()
		var order []string
		handler := func(name string) Handler {
			return func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
				order = append(order, name)
				return next(req)
			}
		}
		pipeline.Alias("a", handler("a")).Alias("b", handler("b"))
		pipeline.PipeByName("a").WrapOuter(handler("outer-1")).PipeByName("b")
		request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil))
		destination := func(*fwcontext.Request) *fwcontext.Response { return fwcontext.NewResponse().Content("ok") }
		pipeline.Then(request, destination) // Materialize a cached order before later mutations.
		order = nil
		pipeline.Unshift(handler("unshift")).WrapOuter(handler("outer-2")).Pipe(handler("late"))
		if withPriority {
			pipeline.SetPriority([]string{"b", "a"})
		}
		pipeline.Then(request, destination)
		want := []string{"outer-1", "outer-2", "unshift", "a", "b", "late"}
		if withPriority {
			want = []string{"outer-1", "outer-2", "b", "a", "unshift", "late"}
		}
		if !reflect.DeepEqual(order, want) {
			t.Fatalf("priority=%t order=%v want=%v", withPriority, order, want)
		}
	}
}

func TestPipelineOuterSurvivesShortCircuitAliasReplacementAndZeroValue(t *testing.T) {
	pipeline := NewPipeline()
	outer, err := NewSecurityHeaders(SecurityHeadersConfig{})
	if err != nil {
		t.Fatal(err)
	}
	pipeline.WrapOuter(nil).WrapOuter(outer)
	pipeline.Alias("security_headers", outer)
	pipeline.Alias("security_headers", func(*fwcontext.Request, func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		return fwcontext.NewResponse().Code(http.StatusForbidden)
	})
	pipeline.PipeByName("security_headers").SetPriority([]string{"security_headers"})
	response := pipeline.Then(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil)), func(*fwcontext.Request) *fwcontext.Response { t.Fatal("short circuit reached destination"); return nil })
	if response.GetStatus() != http.StatusForbidden || response.Headers().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("alias replacement removed frozen outer handler")
	}
	var missing *Pipeline
	if missing.WrapOuter(outer) != nil {
		t.Fatal("nil pipeline changed")
	}
	var zero Pipeline
	zero.WrapOuter(nil).WrapOuter(outer).Unshift(func(*fwcontext.Request, func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		return fwcontext.NewResponse().Code(http.StatusForbidden)
	})
	response = zero.Then(fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil)), nil)
	if response.GetStatus() != http.StatusForbidden || response.Headers().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("zero-value outer wrapper was bypassed by Unshift")
	}
}

func TestPipelineOuterLifecycleCollectsTerminationAfterShortCircuit(t *testing.T) {
	pipeline := NewPipeline()
	terminated := false
	pipeline.WrapOuter(Lifecycle(func(req *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		return next(req)
	}, func(*fwcontext.Request, *fwcontext.Response) { terminated = true }))
	pipeline.Unshift(func(*fwcontext.Request, func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		return fwcontext.NewResponse().Code(http.StatusForbidden)
	})
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil))
	response, terminators := pipeline.ThenWithTerminators(request, nil)
	if response.GetStatus() != http.StatusForbidden || len(terminators) != 1 || terminated {
		t.Fatal("outer lifecycle termination was lost or invoked early")
	}
	terminators[0](request, response)
	if !terminated {
		t.Fatal("outer lifecycle termination did not run")
	}
}
