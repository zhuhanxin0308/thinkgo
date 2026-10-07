package context_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func ExampleResponse_RedirectLocal() {
	response := fwcontext.NewResponse().RedirectLocal("/account?tab=profile", http.StatusSeeOther)
	writer := httptest.NewRecorder()
	if err := response.Send(writer); err != nil {
		panic(err)
	}
	fmt.Println(writer.Code)
	fmt.Println(writer.Header().Get("Location"))
	// Output:
	// 303
	// /account?tab=profile
}
