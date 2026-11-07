package business

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func testDataset(t *testing.T) Dataset {
	t.Helper()
	limits, err := LoadDataset("../../../contracts/dataset.json")
	if err != nil {
		t.Fatal(err)
	}
	return limits
}

func TestCatalogValidationPreservesStableCursor(t *testing.T) {
	limits := testDataset(t)
	query, err := ParseCatalog(url.Values{
		"category_id": {"3"}, "cursor_price": {"1200"}, "cursor_id": {"25"},
		"min_price": {"100"}, "max_price": {"9000"}, "limit": {"20"},
	}, limits)
	if err != nil || query.CategoryID != 3 || query.CursorPrice != 1200 || query.CursorID != 25 || query.Limit != 20 {
		t.Fatalf("游标契约解析错误: %+v %v", query, err)
	}
	for name, values := range map[string]url.Values{
		"负页数":   {"limit": {"-1"}},
		"超限页数":  {"limit": {"51"}},
		"非整数":   {"limit": {"2.5"}},
		"重复参数":  {"limit": {"20", "30"}},
		"未知字段":  {"unexpected": {"1"}},
		"价格颠倒":  {"min_price": {"1000"}, "max_price": {"1"}},
		"半个游标":  {"cursor_price": {"1200"}},
		"单个零游标": {"cursor_price": {"0"}},
		"前导零":   {"category_id": {"01"}},
		"类别越界":  {"category_id": {"33"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCatalog(values, limits); err == nil {
				t.Fatal("非法查询参数未被拒绝")
			}
		})
	}
}

func TestOrderValidationDoesNotMutateCallerAndRejectsAmbiguity(t *testing.T) {
	limits := testDataset(t)
	input := OrderInput{Items: []OrderItemInput{{ProductID: 7, Quantity: 2}, {ProductID: 3, Quantity: 1}}}
	before := append([]OrderItemInput(nil), input.Items...)
	items, err := ValidateOrder(input, limits)
	if err != nil || items[0].ProductID != 3 || !reflect.DeepEqual(before, input.Items) {
		t.Fatalf("锁顺序或输入所有权错误: items=%+v input=%+v err=%v", items, input, err)
	}
	for name, input := range map[string]OrderInput{
		"空订单":  {},
		"重复商品": {Items: []OrderItemInput{{ProductID: 1, Quantity: 1}, {ProductID: 1, Quantity: 2}}},
		"零数量":  {Items: []OrderItemInput{{ProductID: 1}}},
		"数量超限": {Items: []OrderItemInput{{ProductID: 1, Quantity: 11}}},
		"负商品":  {Items: []OrderItemInput{{ProductID: -1, Quantity: 1}}},
		"过多商品": {Items: make([]OrderItemInput, limits.MaximumOrderItems+1)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateOrder(input, limits); err == nil {
				t.Fatal("非法订单未被拒绝")
			}
		})
	}
}

func TestCommentValidationUsesCharactersAndRejectsInvalidEncoding(t *testing.T) {
	limits := testDataset(t)
	text := strings.Repeat("中", limits.MinimumCommentCharacters)
	actual, err := ValidateComment(CommentInput{Body: "  " + text + "  "}, limits)
	if err != nil || actual != text {
		t.Fatalf("中文字符边界错误: %q %v", actual, err)
	}
	for _, body := range []string{"", "too short", strings.Repeat("中", limits.MaximumCommentCharacters+1), string([]byte{0xff, 0xfe})} {
		if _, err := ValidateComment(CommentInput{Body: body}, limits); err == nil {
			t.Fatal("非法评论未被拒绝")
		}
	}
}

func TestPublicErrorsDoNotExposeDatabaseDetails(t *testing.T) {
	status, response := ErrorResult(assertionError("postgres password=private"))
	if status != 500 || strings.Contains(stringJSON(t, response), "private") {
		t.Fatalf("公开错误泄露内部信息: %d %#v", status, response)
	}
	for _, key := range []string{"", strings.Repeat("g", 32), strings.Repeat("a", 31), strings.Repeat("A", 32)} {
		if ValidateIdempotencyKey(key) == nil {
			t.Fatalf("不规范幂等键未拒绝: %q", key)
		}
	}
	if err := ValidateIdempotencyKey(strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
}

type assertionError string

func (err assertionError) Error() string { return string(err) }
