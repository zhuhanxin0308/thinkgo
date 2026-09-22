package context

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestSelectedSourcesKeepStrictParsing 验证省略快照不等于省略来源解析或改变错误优先级。
func TestSelectedSourcesKeepStrictParsing(t *testing.T) {
	for _, testCase := range []struct {
		name, query, body string
		want              error
	}{
		{name: "查询编码", query: "unused=%zz", body: `{}`, want: ErrInvalidQuery},
		{name: "正文优先", query: "unused=%zz", body: `{"broken":`, want: ErrInvalidJSONBody},
		{name: "重复正文", body: `{"unused":1,"unused":2}`, want: ErrInvalidJSONBody},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			raw := httptest.NewRequest(http.MethodPost, "/?"+testCase.query, strings.NewReader(testCase.body))
			raw.Header.Set("Content-Type", "application/json")
			request := MustNewRequest(raw)
			if _, err := request.SourcesFor(SourceSelection{}); !errors.Is(err, testCase.want) {
				t.Fatalf("省略来源后错误变化: got=%v want=%v", err, testCase.want)
			}
		})
	}
	request := MustNewRequest(httptest.NewRequest(http.MethodGet, "/?unused=%zz", nil)).WithGet(map[string]any{})
	if _, err := request.SourcesFor(SourceSelection{}); err != nil {
		t.Fatalf("显式查询覆盖未屏蔽原始来源: %v", err)
	}
}

// TestSelectedSourcesKeepKeysAndOwnership 验证未选择的正文仅保留根键，所选快照仍与原始请求隔离。
func TestSelectedSourcesKeepKeysAndOwnership(t *testing.T) {
	raw := httptest.NewRequest(http.MethodPost, "/?page=1&page=2", strings.NewReader(`{"child":{"name":"Ada"},"items":[1,2]}`))
	raw.Header.Set("Content-Type", "application/json")
	raw.Header["X-Token"] = []string{"one", "two"}
	raw.Header.Set("Cookie", "session=one; session=two")
	request := MustNewRequest(raw)
	keys, err := request.SourcesFor(SourceSelection{})
	if err != nil || keys.Body != nil || keys.Query != nil || keys.Header != nil || keys.Cookies != nil || !keys.HasBody || len(keys.BodyKeys) != 2 {
		t.Fatalf("省略来源没有仅保留字段元数据: %#v %v", keys, err)
	}
	keys.BodyKeys[0] = "changed"
	selection := SourceSelection{Query: true, Header: true, Cookies: true, Body: true}
	selected, err := request.SourcesFor(selection)
	if err != nil || len(selected.BodyKeys) != 0 || !reflect.DeepEqual(selected.Query["page"], []string{"1", "2"}) || !reflect.DeepEqual(selected.Cookies["session"], []string{"one", "two"}) {
		t.Fatalf("所选来源的多值或字段名变化: %#v %v", selected, err)
	}
	selected.Query["page"][0] = "changed"
	selected.Header["X-Token"][0] = "changed"
	selected.Cookies["session"][0] = "changed"
	selected.Body["child"].(map[string]any)["name"] = "changed"
	selected.Body["items"].([]any)[0] = "changed"
	full, err := request.Sources()
	if err != nil || full.Query.Get("page") != "1" || full.Header.Get("X-Token") != "one" || full.Cookies.Get("session") != "one" || full.Body["child"].(map[string]any)["name"] != "Ada" || full.Body["items"].([]any)[0] == "changed" {
		t.Fatalf("快照修改污染请求或完整来源契约: %#v %v", full, err)
	}
}

// TestSelectedSourcesRespectOverrides 验证覆盖来源与原始来源使用相同选择规则及深拷贝边界。
func TestSelectedSourcesRespectOverrides(t *testing.T) {
	request := MustNewRequest(httptest.NewRequest(http.MethodPost, "/", nil)).
		WithHeader(map[string]string{"Content-Type": "application/json", "X-Token": "current"}).
		WithGet(map[string]any{"page": []string{"3", "4"}}).
		WithCookie(map[string]any{"session": []string{"new", "other"}}).
		WithInput(`{"old":1}`).WithPost(map[string]any{"child": map[string]any{"name": "current"}})
	keys, err := request.SourcesFor(SourceSelection{})
	if err != nil || !reflect.DeepEqual(keys.BodyKeys, []string{"child"}) || !keys.HasBody {
		t.Fatalf("POST 覆盖的字段或存在状态丢失: %#v %v", keys, err)
	}
	selected, err := request.SourcesFor(SourceSelection{Query: true, Header: true, Cookies: true, Body: true})
	if err != nil || selected.Query.Get("page") != "3" || selected.Header.Get("X-Token") != "current" || selected.Cookies.Get("session") != "new" {
		t.Fatalf("覆盖来源选择失败: %#v %v", selected, err)
	}
	selected.Body["child"].(map[string]any)["name"] = "changed"
	selected.Query["page"][0] = "changed"
	selected.Cookies["session"][0] = "changed"
	full, err := request.Sources()
	if err != nil || full.Body["child"].(map[string]any)["name"] != "current" || full.Query.Get("page") != "3" || full.Cookies.Get("session") != "new" {
		t.Fatalf("覆盖来源快照污染内部值: %#v %v", full, err)
	}
	for _, override := range []bool{false, true} {
		raw := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("name=Ada&tags=one&tags=two"))
		raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		formRequest := MustNewRequest(raw)
		if override {
			formRequest.WithInput("name=Ada&tags=one&tags=two")
		}
		keys, err := formRequest.SourcesFor(SourceSelection{})
		if err != nil || keys.Form != nil || len(keys.FormKeys) != 2 {
			t.Fatalf("表单字段元数据丢失: override=%v sources=%#v err=%v", override, keys, err)
		}
		form, err := formRequest.SourcesFor(SourceSelection{Form: true})
		if err != nil || !reflect.DeepEqual(form.Form["tags"], []string{"one", "two"}) || form.FormKeys != nil {
			t.Fatalf("表单多值丢失: %#v %v", form, err)
		}
		form.Form["tags"][0] = "changed"
		again, err := formRequest.SourcesFor(SourceSelection{Form: true})
		if err != nil || again.Form.Get("tags") != "one" {
			t.Fatalf("表单快照相互污染: %#v %v", again, err)
		}
	}
}

// TestSelectedSourcesConcurrentOverrides 验证按需快照与覆盖更新并发时不借出内部映射或切片。
func TestSelectedSourcesConcurrentOverrides(t *testing.T) {
	const iterations = 50
	for _, usePost := range []bool{false, true} {
		request := MustNewRequest(httptest.NewRequest(http.MethodPost, "/", nil)).
			WithHeader(map[string]string{"Content-Type": "application/json"}).WithInput(`{"child":{"name":"one"}}`)
		var group sync.WaitGroup
		group.Go(func() {
			for index := range iterations {
				name := "one"
				if index%2 != 0 {
					name = "two"
				}
				if usePost {
					request.WithPost(map[string]any{"child": map[string]any{"name": name}})
				} else {
					request.WithInput(`{"child":{"name":"` + name + `"}}`)
				}
			}
		})
		for _, selection := range []SourceSelection{{}, {Body: true}} {
			group.Go(func() {
				for range iterations {
					snapshot, err := request.SourcesFor(selection)
					if err != nil {
						t.Errorf("并发快照失败: %v", err)
						return
					}
					if !selection.Body {
						if !reflect.DeepEqual(snapshot.BodyKeys, []string{"child"}) {
							t.Errorf("字段快照变化: %#v", snapshot.BodyKeys)
							return
						}
						snapshot.BodyKeys[0] = "changed"
						continue
					}
					child, ok := snapshot.Body["child"].(map[string]any)
					if !ok || child["name"] != "one" && child["name"] != "two" {
						t.Errorf("正文快照丢失或污染: %#v", snapshot.Body)
						return
					}
					child["name"] = "changed"
				}
			})
		}
		group.Wait()
	}
}
