package context_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func ExampleRequest_Route_authorization() {
	raw := httptest.NewRequest(http.MethodPost, "http://example.test/records/123?id=789", strings.NewReader(`{"id":"456","name":"updated"}`))
	raw.Header.Set("Content-Type", "application/json")
	request := fwcontext.MustNewRequest(raw)
	defer request.Cleanup()
	request.SetRoute("id", "123") // 实际应用由路由匹配器设置。

	id := request.Route("id")
	fmt.Println("route:", id)
	fmt.Println("query:", request.Get("id"))
	fmt.Println("body:", request.Post("id"))
	fmt.Println("merged:", request.Param("id"))

	// 此处模拟已经认证的主体的对象权限。真实应用应查询权限，不能从请求取 owner。
	canUpdate := func(recordID string) bool { return recordID == "123" }
	if !canUpdate(id) {
		fmt.Println("denied")
		return
	}
	fmt.Println("update record:", id) // 使用刚才通过权限检查的同一个 ID。
	// Output:
	// route: 123
	// query: 789
	// body: 456
	// merged: 456
	// update record: 123
}
