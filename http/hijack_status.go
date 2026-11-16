package http

import (
	"fmt"
	"net/http"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

const maxWriterObservationLayers = 16

// RecordHijackedStatus 声明接管后已写出的状态，供日志、指标和结束事件使用。
// 请在成功写出并刷新最终状态行后调用，重复声明同一状态是幂等的。
func RecordHijackedStatus(writer http.ResponseWriter, status int) error {
	if status != http.StatusSwitchingProtocols && (status < http.StatusOK || status > 599) {
		return fmt.Errorf("%w: %d", fwcontext.ErrInvalidResponseStatus, status)
	}
	if isNilHTTPResponseWriter(writer) {
		return http.ErrNotSupported
	}
	tracker := findTrackedResponseWriter(writer)
	if tracker == nil {
		return http.ErrNotSupported
	}
	if !tracker.Hijacked() || tracker.status != 0 && tracker.status != status {
		return ErrResponseBoundaryCrossed
	}
	tracker.status = status
	return nil
}

func findTrackedResponseWriter(writer http.ResponseWriter) *statusTrackingResponseWriter {
	for range maxWriterObservationLayers {
		switch current := writer.(type) {
		case *statusTrackingResponseWriter:
			return current
		case interface{ FrameworkResponseWriter() http.ResponseWriter }:
			writer = current.FrameworkResponseWriter()
		case interface{ Unwrap() http.ResponseWriter }:
			writer = current.Unwrap()
		default:
			return nil
		}
	}
	return nil
}
