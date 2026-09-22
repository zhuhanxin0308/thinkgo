package context

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type coordinatedRequestBody struct {
	reader  *strings.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

// Read 在原始正文访问已经开始后阻塞，允许测试精确安排首次输入覆盖。
func (body *coordinatedRequestBody) Read(buffer []byte) (int, error) {
	body.once.Do(func() { close(body.entered) })
	<-body.release
	return body.reader.Read(buffer)
}

func (*coordinatedRequestBody) Close() error { return nil }

// TestRequestBodyRestoreConcurrentSnapshots 覆盖恢复正文与实体判断的交错，不依赖随机启动顺序。
func TestRequestBodyRestoreConcurrentSnapshots(t *testing.T) {
	const original, replacement = `{"name":"original"}`, `{"name":"replacement"}`
	for _, testCase := range []struct {
		name     string
		json     bool
		override bool
		form     bool
	}{
		{name: "Json与首次覆盖Sources", json: true, override: true},
		{name: "Body与首次覆盖Sources", override: true},
		{name: "Json与非JSON解析", json: true},
		{name: "Body与非JSON解析"},
		{name: "Body与表单解析", form: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			originalBody := original
			if testCase.form {
				originalBody = "name=original"
			}
			source := &coordinatedRequestBody{reader: strings.NewReader(originalBody), entered: make(chan struct{}), release: make(chan struct{})}
			raw := httptest.NewRequest(http.MethodPost, "/body", source)
			raw.ContentLength = -1
			raw.Header.Set("Content-Type", "text/plain")
			if testCase.override {
				raw.Header.Set("Content-Type", "application/json")
			} else if testCase.form {
				raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			request := MustNewRequest(raw)
			results := make(chan error, 2)
			go func() {
				if testCase.json {
					var target strictJSONCustomValue
					err := request.Json(&target)
					if err == nil && target.Raw != originalBody {
						err = fmt.Errorf("已开始的原文绑定发生变化: %q", target.Raw)
					}
					results <- err
					return
				}
				body, err := request.Body()
				if err != nil {
					results <- err
					return
				}
				if string(body) != originalBody {
					results <- fmt.Errorf("已开始的原文读取发生变化: %q", body)
					return
				}
				results <- nil
			}()
			<-source.entered
			if testCase.override {
				request.WithInput(replacement)
			}
			go func() {
				<-source.release
				if testCase.override {
					sources, err := request.Sources()
					if err == nil && (!sources.HasBody || sources.Body["name"] != "replacement") {
						err = fmt.Errorf("覆盖输入快照错误: %#v", sources)
					}
					results <- err
					return
				}
				results <- request.Parse()
			}()
			close(source.release)
			for range 2 {
				if err := <-results; err != nil {
					t.Error(err)
				}
			}
			if !testCase.override {
				body, err := io.ReadAll(request.Raw().Body)
				if err != nil || string(body) != originalBody {
					t.Fatalf("原始正文恢复语义改变: body=%q err=%v", body, err)
				}
				if testCase.form && request.Post("name") != "original" {
					t.Fatal("并发正文读取影响了表单字段")
				}
			}
		})
	}
}
