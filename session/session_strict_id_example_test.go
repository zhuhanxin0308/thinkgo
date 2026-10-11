package session_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	"github.com/zhuhanxin0308/thinkgo/v3/session"
	"github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

func ExampleNewSessionWithConfig_rejectUnknownID() {
	config := session.DefaultConfig()
	config.DriverType = "memory"
	config.RejectUnknownID = true
	config.Secure = true
	backend, err := driver.NewMemoryWithMaxEntries(128)
	if err != nil {
		panic(err)
	}
	factory, err := cookie.NewCookie(nil)
	if err != nil {
		panic(err)
	}
	manager, err := session.NewSessionWithConfig(config, backend, factory)
	if err != nil {
		panic(err)
	}
	defer manager.Close()
	const externalID = "0123456789ABCDEF0123456789ABCDEF"
	raw := httptest.NewRequest(http.MethodGet, "https://example.com/cart", nil)
	raw.AddCookie(&http.Cookie{Name: config.Name, Value: externalID})
	writer := httptest.NewRecorder()
	current, err := manager.NewRequestSession(raw, writer)
	if err != nil {
		panic(err)
	}
	if err := current.Set("cart", "item-24"); err != nil {
		panic(err)
	}
	// Session middleware normally saves before committing the response.
	if err := current.Save(); err != nil {
		panic(err)
	}
	cookies := writer.Result().Cookies()
	_, externalExists, err := backend.Read(externalID)
	if err != nil {
		panic(err)
	}
	fmt.Println(len(cookies) == 1 && cookies[0].Value != externalID)
	fmt.Println(externalExists)
	// Output:
	// true
	// false
}
