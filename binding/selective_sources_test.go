package binding

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/exception"
	"github.com/zhuhanxin0308/thinkgo/framework/validate"
)

// TestBindIgnoresUnselectedSnapshotCost 验证业务未声明的元数据数量不会增加重复绑定的快照分配。
func TestBindIgnoresUnselectedSnapshotCost(t *testing.T) {
	const metadataCount, allocationRuns = 32, 100
	makeRequest := func(withMetadata bool) *fwcontext.Request {
		raw := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"Ada"}`))
		raw.Header.Set("Content-Type", "application/json")
		if withMetadata {
			var query, cookies []string
			for index := range metadataCount {
				name := fmt.Sprintf("unused%d", index)
				query = append(query, name+"=value")
				cookies = append(cookies, name+"=value")
				raw.Header.Set("X-"+name, "value")
			}
			raw.URL.RawQuery = strings.Join(query, "&")
			raw.Header.Set("Cookie", strings.Join(cookies, "; "))
		}
		return fwcontext.MustNewRequest(raw)
	}
	measure := func(request *fwcontext.Request) float64 {
		var target struct {
			Name string `json:"name"`
		}
		if err := Bind(request, &target); err != nil {
			t.Fatal(err)
		}
		return testing.AllocsPerRun(allocationRuns, func() {
			if err := Bind(request, &target); err != nil || target.Name != "Ada" {
				panic("绑定结果异常")
			}
		})
	}
	plain, metadata := measure(makeRequest(false)), measure(makeRequest(true))
	t.Logf("正文 DTO 分配: plain=%.0f metadata=%.0f", plain, metadata)
	if metadata != plain {
		t.Fatalf("未声明的来源仍被复制: plain=%.0f metadata=%.0f", plain, metadata)
	}
}

// TestUnvalidatedNodesAvoidFieldDataMaps 验证无规则宽对象只分配动态字段路径和未知字段排序所需空间。
func TestUnvalidatedNodesAvoidFieldDataMaps(t *testing.T) {
	const allocationRuns = 100
	plan, err := planFor(reflect.TypeFor[cachedPlanWideInput]())
	if err != nil {
		t.Fatal(err)
	}
	object := make(map[string]any, len(plan.fields))
	for _, item := range plan.fields {
		object[item.name] = "value"
	}
	target := reflect.New(plan.typ).Elem()
	allocations := testing.AllocsPerRun(allocationRuns, func() {
		state := bindState{}
		state.assign(plan, target, object, "body.item", 0)
		if err := state.err(); err != nil {
			panic(err)
		}
	})
	// 每个动态字段路径允许一次分配，另保留一个排序键切片；验证数据在无规则节点不应创建。
	allowed := len(plan.fields) + 1
	t.Logf("无规则宽节点分配: actual=%.0f allowed=%d", allocations, allowed)
	if allocations > float64(allowed) {
		t.Fatalf("无验证规则仍创建临时字段映射: allocations=%.0f allowed=%d", allocations, allowed)
	}
}

// TestSelectiveBindingKeepsValidationDependencies 验证根与嵌套校验器都能读取没有规则的关联字段和默认值。
func TestSelectiveBindingKeepsValidationDependencies(t *testing.T) {
	type credentials struct {
		Password string `json:"password" validate:"confirm:confirmation"`
		Confirm  string `json:"confirmation"`
		Role     string `json:"role" default:"staff"`
		Company  string `json:"company" validate:"requireIf:role,staff"`
	}
	type input struct {
		Token     string      `header:"X-Token" validate:"confirm:confirmation"`
		Confirm   string      `query:"confirmation"`
		Nested    credentials `json:"nested"`
		Unchanged string      `json:"unchanged"`
	}
	for _, testCase := range []struct {
		body, query string
		valid       bool
	}{
		{body: `{"nested":{"password":"secret","confirmation":"secret","company":"ACME"}}`, query: "confirmation=test-token", valid: true},
		{body: `{"nested":{"password":"secret","confirmation":"other","company":"ACME"}}`, query: "confirmation=test-token"},
		{body: `{"nested":{"password":"secret","confirmation":"secret"}}`, query: "confirmation=test-token"},
		{body: `{"nested":{"password":"secret","confirmation":"secret","company":"ACME"}}`, query: "confirmation=other"},
	} {
		request := bindingRequest(t, testCase.body)
		request.Raw().URL.RawQuery = testCase.query
		original := input{Unchanged: "retained"}
		target := original
		err := Bind(request, &target)
		if (err == nil) != testCase.valid {
			t.Fatalf("关联字段验证变化: valid=%v err=%v", testCase.valid, err)
		}
		if !testCase.valid && !reflect.DeepEqual(target, original) {
			t.Fatal("验证失败发布了临时目标")
		}
	}
}

// TestSelectiveBindingKeepsUnknownBodyAndOptionPriority 验证仅查询 DTO 仍拒绝 JSON 字段，非法选项优先于输入错误。
func TestSelectiveBindingKeepsUnknownBodyAndOptionPriority(t *testing.T) {
	type queryInput struct {
		Page int `query:"page"`
	}
	request := bindingRequest(t, `{"z":{"large":[1,2,3]},"a":1}`)
	target := queryInput{Page: 9}
	issues := cachedPlanIssues(t, Bind(request, &target))
	if len(issues) != 2 || issues[0].Field != "body.a" || issues[1].Field != "body.z" || target.Page != 9 {
		t.Fatalf("未选择正文失去未知字段检查或失败隔离: %#v %#v", issues, target)
	}
	for _, options := range [][]validate.Option{{nil}, {validate.WithLocation(nil)}, {validate.WithScene("missing")}} {
		if err := Bind(bindingRequest(t, `{"invalid":`), &target, options...); !errors.Is(err, ErrDefinition) {
			t.Fatalf("选项定义错误优先级变化: %v", err)
		}
	}
	request = bindingRequest(t, `{}`)
	request.Raw().URL.RawQuery = "unused=%zz"
	var bodyOnly struct {
		Name string `json:"name"`
	}
	var failure *exception.HttpException
	if err := Bind(request, &bodyOnly); !errors.As(err, &failure) || failure.StatusCode != http.StatusBadRequest {
		t.Fatalf("未声明查询来源跳过了编码检查: %v", err)
	}
	issues = failure.Data["errors"].([]Issue)
	if len(issues) != 1 || issues[0].Field != "query" {
		t.Fatalf("查询编码错误来源变化: %#v", issues)
	}
}
