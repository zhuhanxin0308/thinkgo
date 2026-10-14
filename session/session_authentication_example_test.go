package session_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	"github.com/zhuhanxin0308/thinkgo/v3/session"
	"github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

func ExampleSession_SetAuthenticated() {
	config := session.DefaultConfig()
	config.DriverType, config.RejectUnknownID = "memory", true
	factory, err := cookie.NewCookie(nil)
	if err != nil {
		panic(err)
	}
	manager, err := session.NewSessionWithConfig(config, driver.NewMemory(), factory)
	if err != nil {
		panic(err)
	}
	defer manager.Close()
	writer := httptest.NewRecorder()
	current, err := manager.NewRequestSession(httptest.NewRequest(http.MethodPost, "https://example.test/login", nil), writer)
	if err != nil {
		panic(err)
	}
	// The application has already verified credentials and account authorization.
	verifiedIdentity := map[string]string{"uid": "account-24"}
	if err := current.SetAuthenticated("identity", verifiedIdentity); err != nil {
		// Fail the login; do not send a success response.
		panic(err)
	}
	// Session middleware normally performs this step before committing a response.
	if err := current.Save(); err != nil {
		panic(err)
	}
	raw := httptest.NewRequest(http.MethodGet, "https://example.test/account", nil)
	raw.AddCookie(writer.Result().Cookies()[0])
	restored, err := manager.NewRequestSession(raw, httptest.NewRecorder())
	if err != nil {
		panic(err)
	}
	identity, found := restored.Get("identity")
	fmt.Println(found && identity.(map[string]interface{})["uid"] == "account-24")
	// Output: true
}
