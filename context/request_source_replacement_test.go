package context

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type coordinatedSourceQuery struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

// String 在首次正文解析成功后阻塞查询快照，精确安排后续输入覆盖而不改动生产代码。
func (value *coordinatedSourceQuery) String() string {
	value.once.Do(func() { close(value.entered) })
	<-value.release
	return "ready"
}

type sourceReplacementResult struct {
	snapshot SelectedRequestSources
	err      error
}

func sourcesAcrossReplacement(t *testing.T, request *Request, mode string, replace func()) sourceReplacementResult {
	t.Helper()
	const synchronizationTimeout = 5 * time.Second
	query := &coordinatedSourceQuery{entered: make(chan struct{}), release: make(chan struct{})}
	request.WithGet(map[string]any{"coordinate": query})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(query.release) }) }
	t.Cleanup(release)
	results := make(chan sourceReplacementResult, 1)
	go func() {
		if mode == "full" {
			snapshot, err := request.Sources()
			results <- sourceReplacementResult{snapshot: SelectedRequestSources{RequestSources: snapshot}, err: err}
			return
		}
		selection := SourceSelection{Query: true, Body: mode == "values", Form: mode == "values"}
		snapshot, err := request.SourcesFor(selection)
		results <- sourceReplacementResult{snapshot: snapshot, err: err}
	}()
	select {
	case <-query.entered:
	case result := <-results:
		t.Fatalf("进入查询同步点前意外退出: %v", result.err)
	case <-time.After(synchronizationTimeout):
		t.Fatal("未进入查询同步点")
	}
	replace()
	release()
	select {
	case result := <-results:
		return result
	case <-time.After(synchronizationTimeout):
		t.Fatal("输入覆盖后快照未完成")
		return sourceReplacementResult{}
	}
}

// TestSourcesRejectReplacementParseErrors 验证两次正文读取之间出现的新错误不能伪装成成功的空快照。
func TestSourcesRejectReplacementParseErrors(t *testing.T) {
	const maximumBodyBytes = 32
	for _, testCase := range []struct {
		name, media, original, replacement string
		want                               error
		priorPost                          bool
	}{
		{name: "json", media: "application/json", original: `{"name":"valid"}`, replacement: `{"name":`, want: ErrInvalidJSONBody},
		{name: "form", media: "application/x-www-form-urlencoded", original: "name=valid", replacement: "name=%zz", want: ErrInvalidFormBody},
		{name: "limit", media: "application/json", original: `{"name":"valid"}`, replacement: strings.Repeat(" ", maximumBodyBytes+1), want: ErrRequestBodyTooLarge},
		{name: "post_reset", media: "application/json", original: `{"name":"valid"}`, replacement: `{"name":`, want: ErrInvalidJSONBody, priorPost: true},
	} {
		for _, mode := range []string{"values", "keys", "full"} {
			t.Run(testCase.name+"/"+mode, func(t *testing.T) {
				raw := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(testCase.original))
				raw.Header.Set("Content-Type", testCase.media)
				request := MustNewRequest(raw, WithMaxBodyBytes(maximumBodyBytes))
				if testCase.priorPost {
					request.WithPost(map[string]any{"name": "previous"})
				}
				result := sourcesAcrossReplacement(t, request, mode, func() { request.WithInput(testCase.replacement) })
				if !errors.Is(result.err, testCase.want) {
					t.Fatalf("并发输入覆盖的解析错误被吞掉: got=%v want=%v snapshot=%#v", result.err, testCase.want, result.snapshot)
				}
				if result.snapshot.Body != nil || result.snapshot.Form != nil || result.snapshot.BodyKeys != nil || result.snapshot.FormKeys != nil {
					t.Fatalf("错误输入仍发布正文快照: %#v", result.snapshot)
				}
			})
		}
	}
}

// TestSourcesReplacementKeepsExplicitPostPriority 验证成功初次解析后的显式 POST 覆盖继续优先于输入快照。
func TestSourcesReplacementKeepsExplicitPostPriority(t *testing.T) {
	for _, initialPost := range []bool{false, true} {
		for _, mode := range []string{"values", "keys", "full"} {
			raw := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"valid"}`))
			raw.Header.Set("Content-Type", "application/json")
			request := MustNewRequest(raw)
			if initialPost {
				request.WithPost(map[string]any{"name": "explicit"})
			}
			result := sourcesAcrossReplacement(t, request, mode, func() {
				if !initialPost {
					request.WithInput(`{"name":`)
					request.WithPost(map[string]any{"name": "explicit"})
				}
			})
			if result.err != nil || !result.snapshot.HasBody {
				t.Fatalf("显式 POST 覆盖失去优先级: initial=%v mode=%s result=%#v", initialPost, mode, result)
			}
			if mode == "keys" {
				if !reflect.DeepEqual(result.snapshot.BodyKeys, []string{"name"}) {
					t.Fatalf("显式 POST 字段快照丢失: %#v", result.snapshot)
				}
			} else if result.snapshot.Body["name"] != "explicit" {
				t.Fatalf("显式 POST 值未被采用: %#v", result.snapshot)
			}
		}
	}
}

// TestSourcesReplacementKeepsInitialErrorPriority 验证快照优化不绕过首次正文及查询编码检查。
func TestSourcesReplacementKeepsInitialErrorPriority(t *testing.T) {
	for _, testCase := range []struct {
		body  string
		input string
		want  error
	}{
		{body: `{"name":`, want: ErrInvalidJSONBody},
		{body: `{"name":"valid"}`, input: `{"name":`, want: ErrInvalidJSONBody},
		{body: `{"name":"valid"}`, want: ErrInvalidQuery},
	} {
		raw := httptest.NewRequest(http.MethodPost, "/?invalid=%zz", strings.NewReader(testCase.body))
		raw.Header.Set("Content-Type", "application/json")
		request := MustNewRequest(raw)
		if testCase.input != "" {
			request.WithInput(testCase.input)
		}
		request.WithPost(map[string]any{"name": "explicit"})
		if _, err := request.SourcesFor(SourceSelection{}); !errors.Is(err, testCase.want) {
			t.Fatalf("首次解析错误被显式 POST 屏蔽或顺序改变: got=%v want=%v", err, testCase.want)
		}
	}
}
