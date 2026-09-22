package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/exception"
	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
)

type failingCredentialStore struct{ err error }

func (store failingCredentialStore) LookupToken(context.Context, [sha256.Size]byte) (Identity, bool, error) {
	return Identity{}, false, store.err
}

type failingAuthorizer struct{ err error }

func (authorizer failingAuthorizer) Authorize(context.Context, Identity, string) error {
	return authorizer.err
}

type capturedAuthFailure struct{ reported error }

func (handler *capturedAuthFailure) Report(value any) { handler.reported, _ = value.(error) }
func (handler *capturedAuthFailure) Render(writer http.ResponseWriter, request *http.Request, value any) error {
	return (&exception.Handle{}).Render(writer, request, value)
}

// TestAuthenticationInfrastructureErrorsReachRecovery 验证存储故障与超时保留原始错误链，并通过统一异常链返回 500。
func TestAuthenticationInfrastructureErrorsReachRecovery(t *testing.T) {
	for _, cause := range []error{context.DeadlineExceeded, errors.New("凭据存储不可用")} {
		raw := httptest.NewRequest(http.MethodGet, "/", nil)
		raw.Header.Set("Authorization", "Bearer secret-token")
		request := fwcontext.MustNewRequest(raw)
		authenticator, err := Bearer(failingCredentialStore{err: cause})
		if err != nil {
			t.Fatal(err)
		}
		reported := &capturedAuthFailure{}
		recovery := &middleware.Recovery{Handler: reported}
		response := recovery.Handle(request, func(request *fwcontext.Request) *fwcontext.Response {
			return Authenticate(authenticator)(request, func(*fwcontext.Request) *fwcontext.Response {
				t.Fatal("认证故障不应执行下游")
				return nil
			})
		})
		if response.GetStatus() != http.StatusInternalServerError || !errors.Is(reported.reported, cause) {
			t.Fatalf("基础设施故障被当成认证拒绝: 状态 %d，记录 %v", response.GetStatus(), reported.reported)
		}
	}
}

// TestAuthorizationDistinguishesDenialFromFailure 验证业务拒绝保持 403，后端故障则进入统一异常处理。
func TestAuthorizationDistinguishesDenialFromFailure(t *testing.T) {
	for _, item := range []struct {
		cause  error
		status int
	}{
		{fmt.Errorf("角色检查: %w", ErrForbidden), http.StatusForbidden},
		{context.DeadlineExceeded, http.StatusInternalServerError},
	} {
		request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil))
		request.Set(requestIdentityKey, Identity{Subject: "user"})
		reported := &capturedAuthFailure{}
		response := (&middleware.Recovery{Handler: reported}).Handle(request, func(request *fwcontext.Request) *fwcontext.Response {
			return Require(failingAuthorizer{err: item.cause}, "users.read")(request, func(*fwcontext.Request) *fwcontext.Response {
				t.Fatal("授权失败不应执行下游")
				return nil
			})
		})
		if response.GetStatus() != item.status || item.status == http.StatusInternalServerError && !errors.Is(reported.reported, item.cause) {
			t.Fatalf("授权错误分类错误: 状态 %d，记录 %v", response.GetStatus(), reported.reported)
		}
	}
}
