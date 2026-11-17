package otlp

import (
	"fmt"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func newSamplingPolicy(ratio float64, draw func() float64) sdktrace.Sampler {
	remote := remoteParentSampler{ratio: ratio, draw: draw}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio),
		sdktrace.WithRemoteParentSampled(remote), sdktrace.WithRemoteParentNotSampled(remote))
}

type remoteParentSampler struct {
	ratio float64
	draw  func() float64
}

func (sampler remoteParentSampler) ShouldSample(parameters sdktrace.SamplingParameters) sdktrace.SamplingResult {
	decision := sdktrace.Drop
	// 外部请求可以选择 TraceID 和采样标志，远程父链路只能参与关联。
	if sampler.ratio >= 1 || sampler.ratio > 0 && sampler.draw() < sampler.ratio {
		decision = sdktrace.RecordAndSample
	}
	return sdktrace.SamplingResult{
		Decision:   decision,
		Tracestate: trace.SpanContextFromContext(parameters.ParentContext).TraceState(),
	}
}

func (sampler remoteParentSampler) Description() string {
	return fmt.Sprintf("ThinkGoRemoteParentRatio(%g)", sampler.ratio)
}
