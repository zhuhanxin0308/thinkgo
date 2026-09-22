package binding

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
)

type cachedPlanChild struct {
	Name    string         `json:"name" default:"guest"`
	Rank    int            `json:"rank" default:"1"`
	Enabled Optional[bool] `json:"enabled"`
}

type cachedPlanInput struct {
	Input
	ID      int               `path:"id"`
	Limit   int               `query:"limit" default:"20"`
	Tenant  string            `header:"X-Tenant"`
	Session string            `cookie:"session"`
	Enabled Optional[bool]    `json:"enabled"`
	Child   *cachedPlanChild  `json:"child"`
	Items   []cachedPlanChild `json:"items"`
}

// TestCachedPlanKeepsSourceFilteringAndErrorOrder 验证静态字段计划不能把外部来源字段放入 JSON 白名单。
func TestCachedPlanKeepsSourceFilteringAndErrorOrder(t *testing.T) {
	request := bindingRequest(t, `{"child":{"z":0,"a":0},"items":[{"x":0}],"limit":4,"session":"forged","id":9,"X-Tenant":"forged","zzz":0}`)
	original := cachedPlanInput{ID: 7, Limit: 8, Child: &cachedPlanChild{Name: "original"}, Items: []cachedPlanChild{{Name: "retained"}}}
	target := original
	err := Bind(request, &target)
	issues := cachedPlanIssues(t, err)
	expected := []string{"body.child.a", "body.child.z", "body.items[0].x", "body.X-Tenant", "body.id", "body.limit", "body.session", "body.zzz"}
	actual := make([]string, len(issues))
	for index, issue := range issues {
		actual[index] = issue.Field
		if issue.Rule != "unknown" {
			t.Fatalf("未知字段错误类型变化: %#v", issue)
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("嵌套检查及根字段错误顺序变化: got=%v want=%v", actual, expected)
	}
	if !reflect.DeepEqual(target, original) {
		t.Fatal("失败绑定不得发布部分字段或修改原有嵌套对象")
	}
	request.WithInput(`{"child":{},"items":[{}]}`)
	if err := Bind(request, &target); err != nil || target.ID != 42 || target.Child.Name != "guest" || target.Items[0].Rank != 1 {
		t.Fatalf("失败后的计划不能保留用户字段或污染后续成功绑定: target=%#v err=%v", target, err)
	}
}

// TestCachedPlanKeepsFormSourceFiltering 验证表单未知字段仍按键排序，查询参数不能成为表单白名单。
func TestCachedPlanKeepsFormSourceFiltering(t *testing.T) {
	var target struct {
		Name string `form:"name"`
		Page int    `query:"page" default:"1"`
	}
	raw := httptest.NewRequest(http.MethodPost, "/?page=2", strings.NewReader("name=Ada&z=1&page=3&a=4"))
	raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	issues := cachedPlanIssues(t, Bind(fwcontext.MustNewRequest(raw), &target))
	var actual []string
	for _, issue := range issues {
		actual = append(actual, issue.Field)
	}
	if !reflect.DeepEqual(actual, []string{"form.a", "form.page", "form.z"}) {
		t.Fatalf("表单来源边界或错误顺序变化: %v", actual)
	}
}

// TestCachedPlanPreservesDefaultsAndPatchPresence 验证共享计划不保留上次请求的存在状态、null 或字段值。
func TestCachedPlanPreservesDefaultsAndPatchPresence(t *testing.T) {
	for _, testCase := range []struct {
		body    string
		present bool
		null    bool
	}{
		{body: `{"child":{},"items":[{}]}`},
		{body: `{"enabled":null,"child":{"enabled":null},"items":[{}]}`, present: true, null: true},
		{body: `{"enabled":false,"child":{"enabled":false},"items":[{}]}`, present: true},
		{body: `{"child":{},"items":[{}]}`},
	} {
		var target cachedPlanInput
		if err := Bind(bindingRequest(t, testCase.body), &target); err != nil {
			t.Fatal(err)
		}
		if target.Limit != 20 || target.Child.Name != "guest" || target.Child.Rank != 1 || target.Items[0].Name != "guest" {
			t.Fatalf("缺失字段默认值变化: %#v", target)
		}
		for _, value := range []Optional[bool]{target.Enabled, target.Child.Enabled} {
			if value.IsSet() != testCase.present || value.IsNull() != testCase.null || value.Value() {
				t.Fatalf("PATCH 三态变化: body=%s value=%#v", testCase.body, value)
			}
		}
	}
}

// TestCachedPlanConcurrentBindings 验证同一类型计划在独立请求间并发复用不共享用户数据。
func TestCachedPlanConcurrentBindings(t *testing.T) {
	const workers, iterations = 8, 30
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Go(func() {
			for index := 0; index < iterations; index++ {
				var target cachedPlanInput
				if err := Bind(bindingRequest(t, `{"child":{},"items":[{}]}`), &target); err != nil || target.Child.Name != "guest" {
					t.Errorf("并发绑定失败: target=%#v err=%v", target, err)
					return
				}
			}
		})
	}
	group.Wait()
}

func cachedPlanIssues(t *testing.T, err error) []Issue {
	t.Helper()
	var failure *exception.HttpException
	if !errors.As(err, &failure) || failure.StatusCode != http.StatusBadRequest {
		t.Fatalf("应返回请求参数错误: %v", err)
	}
	issues, ok := failure.Data["errors"].([]Issue)
	if !ok {
		t.Fatalf("缺少结构化字段错误: %#v", failure.Data)
	}
	return issues
}

type cachedPlanWideInput struct {
	Name        string `json:"name"`
	SKU         string `json:"sku"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Brand       string `json:"brand"`
	Barcode     string `json:"barcode"`
	Unit        string `json:"unit"`
	Origin      string `json:"origin"`
	Supplier    string `json:"supplier"`
	Warehouse   string `json:"warehouse"`
	Location    string `json:"location"`
	Color       string `json:"color"`
	Size        string `json:"size"`
	Material    string `json:"material"`
	Model       string `json:"model"`
	Remark      string `json:"remark"`
}

// TestCachedPlanAbsentFieldsDoNotAllocateSets 验证未提交字段不再分配静态白名单或固定错误路径。
func TestCachedPlanAbsentFieldsDoNotAllocateSets(t *testing.T) {
	const runs, targetAllocationAllowance = 100, 1
	request := bindingRequest(t, `{}`)
	// 两个目标都声明正文来源，避免把按需来源快照的差异误算成字段数量开销。
	var empty struct {
		Name string `json:"name"`
	}
	var wide cachedPlanWideInput
	for _, target := range []any{&empty, &wide} {
		if err := Bind(request, target); err != nil {
			t.Fatal(err)
		}
	}
	measure := func(target any) float64 {
		return testing.AllocsPerRun(runs, func() {
			if err := Bind(request, target); err != nil {
				panic(err)
			}
		})
	}
	emptyAllocations, wideAllocations := measure(&empty), measure(&wide)
	t.Logf("缺失字段绑定分配: empty=%.0f wide=%.0f", emptyAllocations, wideAllocations)
	// 宽对象仅允许增加非空目标快照；字段声明和错误路径均属于静态计划。
	if wideAllocations > emptyAllocations+targetAllocationAllowance {
		t.Fatalf("字段声明不应重复分配静态数据: empty=%.0f wide=%.0f allowance=%d", emptyAllocations, wideAllocations, targetAllocationAllowance)
	}
}

type CachedPlanEmbeddedSources struct {
	Tenant int `header:"x-tenant"`
	Page   int `query:"page"`
}

// TestCachedRootPathsKeepEmbeddedAndNestedCoordinates 验证提升字段按来源定位，数组索引仍使用动态前缀。
func TestCachedRootPathsKeepEmbeddedAndNestedCoordinates(t *testing.T) {
	var target struct {
		*CachedPlanEmbeddedSources
		ID    int `path:"id"`
		Items []struct {
			Quantity int `json:"quantity"`
		} `json:"items"`
	}
	raw := httptest.NewRequest(http.MethodPost, "/?page=invalid", strings.NewReader(`{"items":[{"quantity":"invalid"},{"quantity":null}]}`))
	raw.Header.Set("Content-Type", "application/json")
	raw.Header.Set("X-Tenant", "invalid")
	issues := cachedPlanIssues(t, Bind(fwcontext.MustNewRequest(raw), &target))
	var actual []string
	for _, issue := range issues {
		actual = append(actual, issue.Field)
	}
	expected := []string{"header.X-Tenant", "query.page", "path.id", "body.items[0].quantity", "body.items[1].quantity"}
	if !reflect.DeepEqual(actual, expected) || target.CachedPlanEmbeddedSources != nil {
		t.Fatalf("错误坐标或失败原子性变化: got=%v target=%#v", actual, target)
	}
}

// TestCachedRootValidationPathsKeepSources 验证根验证器仍使用规范化请求头及原始来源名称。
func TestCachedRootValidationPathsKeepSources(t *testing.T) {
	var target struct {
		Tenant string `header:"x-tenant" validate:"required"`
		Token  string `cookie:"token" validate:"required"`
		Name   string `json:"name" validate:"required"`
	}
	err := Bind(bindingRequest(t, `{}`), &target)
	var failure *exception.HttpException
	if !errors.As(err, &failure) || failure.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("缺失字段应保持验证错误分类: %v", err)
	}
	issues, ok := failure.Data["errors"].([]Issue)
	if !ok {
		t.Fatalf("缺少结构化验证错误: %#v", failure.Data)
	}
	var actual []string
	for _, issue := range issues {
		actual = append(actual, issue.Field)
	}
	expected := []string{"header.X-Tenant", "body.name", "cookie.token"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("根验证路径或验证器错误顺序变化: got=%v want=%v", actual, expected)
	}
}

// BenchmarkBindCachedPlanAssignment 直接使用固定输入对象与类型计划，排除 Request.Sources 和请求解析的成本变化。
func BenchmarkBindCachedPlanAssignment(b *testing.B) {
	plan, err := planFor(reflect.TypeFor[cachedPlanWideInput]())
	if err != nil {
		b.Fatal(err)
	}
	object := map[string]any{"name": "updated", "sku": "SKU-001"}
	target := reflect.New(plan.typ).Elem()
	state := bindState{}
	state.assign(plan, target, object, "body.item", 0)
	if err := state.err(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		state := bindState{}
		state.assign(plan, target, object, "body.item", 0)
		if err := state.err(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBindCachedPlanDTO 使用已解析请求测量类型计划复用，覆盖宽对象、嵌套数组和表单三种业务形状。
func BenchmarkBindCachedPlanDTO(b *testing.B) {
	for _, scenario := range []struct {
		name, media, body string
		target            any
	}{
		{name: "wide_patch", media: "application/json", body: `{"name":"updated","sku":"SKU-001"}`, target: &cachedPlanWideInput{}},
		{name: "nested_items", media: "application/json", body: `{"items":[{"name":"one","sku":"SKU-001"},{"name":"two","sku":"SKU-002"},{"name":"three","sku":"SKU-003"}]}`, target: &struct {
			Items []cachedPlanWideInput `json:"items"`
		}{}},
		{name: "form", media: "application/x-www-form-urlencoded", body: "name=Ada&sku=SKU-001&tags=one&tags=two", target: &struct {
			Name string   `form:"name"`
			SKU  string   `form:"sku"`
			Tags []string `form:"tags"`
		}{}},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			raw := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(scenario.body))
			raw.Header.Set("Content-Type", scenario.media)
			request := fwcontext.MustNewRequest(raw)
			if err := Bind(request, scenario.target); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := Bind(request, scenario.target); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
