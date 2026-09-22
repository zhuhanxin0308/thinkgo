package context

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type streamControlWriter struct {
	*httptest.ResponseRecorder
	readDeadline  time.Time
	writeDeadline time.Time
	flushErr      error
	informational []int
}

func (writer *streamControlWriter) WriteHeader(status int) {
	if status >= http.StatusContinue && status < http.StatusOK && status != http.StatusSwitchingProtocols {
		writer.informational = append(writer.informational, status)
		return
	}
	writer.ResponseRecorder.WriteHeader(status)
}

func (writer *streamControlWriter) SetReadDeadline(deadline time.Time) error {
	writer.readDeadline = deadline
	return nil
}

func (writer *streamControlWriter) SetWriteDeadline(deadline time.Time) error {
	writer.writeDeadline = deadline
	return nil
}

func (writer *streamControlWriter) FlushError() error {
	writer.ResponseRecorder.Flush()
	return writer.flushErr
}

// TestStreamResponseControllerPreservesCapabilities 验证业务流可设置响应头、状态和逐请求期限，并及时刷新首段。
func TestStreamResponseControllerPreservesCapabilities(t *testing.T) {
	writer := &streamControlWriter{ResponseRecorder: httptest.NewRecorder()}
	deadline := time.Now().Add(time.Second)
	response := NewResponse().Stream(func(destination io.Writer) error {
		responseWriter, ok := destination.(http.ResponseWriter)
		if !ok {
			t.Fatal("流写入器必须支持标准 ResponseWriter")
		}
		responseWriter.Header().Set("Content-Type", "text/event-stream")
		controller := http.NewResponseController(responseWriter)
		if err := controller.SetReadDeadline(deadline); err != nil {
			return err
		}
		if err := controller.SetWriteDeadline(deadline); err != nil {
			return err
		}
		responseWriter.WriteHeader(http.StatusAccepted)
		responseWriter.WriteHeader(http.StatusInternalServerError)
		if _, err := io.WriteString(destination, "data: first\n\n"); err != nil {
			return err
		}
		return controller.Flush()
	})
	if err := response.Send(writer); err != nil {
		t.Fatal(err)
	}
	if writer.Code != http.StatusAccepted || writer.Header().Get("Content-Type") != "text/event-stream" || !writer.Flushed || writer.Body.String() != "data: first\n\n" {
		t.Fatalf("流控制操作没有保持响应契约: %+v", writer.ResponseRecorder)
	}
	if !writer.readDeadline.Equal(deadline) || !writer.writeDeadline.Equal(deadline) {
		t.Fatal("流包装器没有向底层传递期限")
	}
}

// TestStreamIgnoredFlushFailureStillFailsSend 验证兼容 http.Flusher 的无返回值刷新不会丢掉传输失败。
func TestStreamIgnoredFlushFailureStillFailsSend(t *testing.T) {
	failure := errors.New("connection write failed")
	writer := &streamControlWriter{ResponseRecorder: httptest.NewRecorder(), flushErr: failure}
	err := NewResponse().Stream(func(destination io.Writer) error {
		destination.(http.Flusher).Flush()
		return nil
	}).Send(writer)
	if !errors.Is(err, failure) || !IsResponseTransmissionError(err) {
		t.Fatalf("被回调忽略的刷新错误也必须由 Send 报告: %v", err)
	}
}

// TestStreamInformationalStatusDoesNotCommitFinalStatus 验证 103 提示之后仍能选择最终状态。
func TestStreamInformationalStatusDoesNotCommitFinalStatus(t *testing.T) {
	writer := &streamControlWriter{ResponseRecorder: httptest.NewRecorder()}
	err := NewResponse().Stream(func(destination io.Writer) error {
		responseWriter := destination.(http.ResponseWriter)
		responseWriter.WriteHeader(http.StatusEarlyHints)
		responseWriter.WriteHeader(http.StatusAccepted)
		_, err := io.WriteString(destination, "accepted")
		return err
	}).Send(writer)
	if err != nil || len(writer.informational) != 1 || writer.Code != http.StatusAccepted || writer.Body.String() != "accepted" {
		t.Fatalf("提示响应不得封闭最终响应: hints=%v status=%d body=%q err=%v", writer.informational, writer.Code, writer.Body.String(), err)
	}
}
