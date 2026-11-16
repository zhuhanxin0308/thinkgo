package context

import (
	"errors"
	"net/http"
	"strconv"
)

const maxResponseWriterLayers = 16

type responseBodyPolicy interface {
	BodySuppressed() bool
}

// findResponseBodyPolicy 穿透已知写入器适配层；限制层数避免外部包装形成循环。
func findResponseBodyPolicy(w http.ResponseWriter) responseBodyPolicy {
	for range maxResponseWriterLayers {
		if policy, ok := w.(responseBodyPolicy); ok {
			return policy
		}
		if bridge, ok := w.(interface{ FrameworkResponseWriter() http.ResponseWriter }); ok {
			w = bridge.FrameworkResponseWriter()
			continue
		}
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = wrapper.Unwrap()
	}
	return nil
}

func setHeadContentLength(w http.ResponseWriter, policy responseBodyPolicy, status int, size int64, sample []byte) {
	header := w.Header()
	if _, exists := header["Content-Length"]; exists {
		return
	}
	if encoder, ok := policy.(interface {
		CanReportHeadLength(status int, size int64, sample []byte) bool
	}); ok && !encoder.CanReportHeadLength(status, size, sample) {
		return
	}
	header.Set("Content-Length", strconv.FormatInt(size, 10))
}

func (r *Response) commitHead(w http.ResponseWriter) error {
	w.WriteHeader(r.status)
	if provider, ok := w.(interface{ CommitError() error }); ok {
		return errors.Join(r.err, markResponseTransmissionError(provider.CommitError()))
	}
	return r.err
}
