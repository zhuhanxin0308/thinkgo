package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

// TestIdentityRejectsNormalizedClaimAndAttributeCollisions 验证规范化后的重复声明不会被静默合并或覆盖。
func TestIdentityRejectsNormalizedClaimAndAttributeCollisions(t *testing.T) {
	testCases := []struct {
		name        string
		roles       []string
		permissions []string
		attributes  map[string]string
	}{
		{name: "角色碰撞", roles: []string{"admin", " admin "}},
		{name: "权限碰撞", permissions: []string{"users.read", " users.read "}},
		{name: "属性键碰撞", attributes: map[string]string{"tenant": "a", " tenant ": "b"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewIdentity("user", testCase.roles, testCase.permissions, testCase.attributes)
			if !errors.Is(err, ErrIdentityCollision) || !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("规范化碰撞必须返回稳定错误，实际为 %v", err)
			}
		})
	}
}

// TestRBACRejectsNormalizedRoleCollisions 验证角色配置结果不再依赖 Go map 的随机遍历顺序。
func TestRBACRejectsNormalizedRoleCollisions(t *testing.T) {
	_, err := NewRBAC(map[string][]string{
		"admin":   {"users.read"},
		" admin ": {"users.write"},
	})
	if !errors.Is(err, ErrIdentityCollision) || !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("RBAC 规范化角色碰撞必须失败关闭，实际为 %v", err)
	}
}

// TestRequireRejectsNilNextWithoutPanic 验证授权成功后缺少下游处理器会返回稳定的服务端错误。
func TestRequireRejectsNilNextWithoutPanic(t *testing.T) {
	rbac, err := NewRBAC(map[string][]string{"reader": {"users.read"}})
	if err != nil {
		t.Fatalf("创建 RBAC 失败: %v", err)
	}
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil))
	identity, err := NewIdentity("user", []string{"reader"}, nil, nil)
	if err != nil {
		t.Fatalf("创建认证主体失败: %v", err)
	}
	request.Set(requestIdentityKey, identity)
	response := Require(rbac, "users.read")(request, nil)
	if response.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("缺少下游处理器应返回 500，实际为 %d", response.GetStatus())
	}
}

type invalidTokenStore struct{}

func (*invalidTokenStore) LookupToken(context.Context, [sha256.Size]byte) (Identity, bool, error) {
	return Identity{Subject: "invalid\nsubject"}, true, nil
}

// TestBearerAuthenticationAndRBACMiddleware 验证 token 摘要认证、请求主体和角色权限链。
func TestBearerAuthenticationAndRBACMiddleware(t *testing.T) {
	identity, err := NewIdentity("user-42", []string{"admin"}, nil, map[string]string{"tenant": "a"})
	if err != nil {
		t.Fatalf("创建认证主体失败: %v", err)
	}
	store, err := NewStaticTokenStore(map[string]Identity{"secret-token": identity})
	if err != nil {
		t.Fatalf("创建 token 存储失败: %v", err)
	}
	authenticator, err := Bearer(store)
	if err != nil {
		t.Fatalf("创建 Bearer 认证器失败: %v", err)
	}
	rbac, err := NewRBAC(map[string][]string{"admin": {"users.read"}})
	if err != nil {
		t.Fatalf("创建 RBAC 失败: %v", err)
	}
	raw := httptest.NewRequest(http.MethodGet, "/users", nil)
	raw.Header.Set("Authorization", "Bearer secret-token")
	request := fwcontext.MustNewRequest(raw)
	response := Authenticate(authenticator)(request, func(current *fwcontext.Request) *fwcontext.Response {
		return Require(rbac, "users.read")(current, func(inner *fwcontext.Request) *fwcontext.Response {
			resolved, exists := IdentityFromRequest(inner)
			if !exists || resolved.Subject != "user-42" || resolved.Attributes["tenant"] != "a" {
				t.Fatalf("请求认证主体错误: %#v", resolved)
			}
			return fwcontext.NewResponse().Content("ok")
		})
	})
	if response.GetStatus() != http.StatusOK || string(response.GetBody()) != "ok" {
		t.Fatalf("认证授权响应错误: status=%d body=%q", response.GetStatus(), response.GetBody())
	}
}

// TestBearerRejectsAmbiguousAndInvalidCredentials 验证重复头、错误 scheme 和未知 token 都统一返回 401。
func TestBearerRejectsAmbiguousAndInvalidCredentials(t *testing.T) {
	identity, _ := NewIdentity("user", nil, nil, nil)
	store, _ := NewStaticTokenStore(map[string]Identity{"valid": identity})
	authenticator, _ := Bearer(store)
	for _, values := range [][]string{{}, {"Basic valid"}, {"Bearer unknown"}, {"Bearer valid", "Bearer valid"}} {
		raw := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, value := range values {
			raw.Header.Add("Authorization", value)
		}
		request := fwcontext.MustNewRequest(raw)
		response := Authenticate(authenticator)(request, func(*fwcontext.Request) *fwcontext.Response {
			t.Fatal("非法凭据不应进入下游")
			return nil
		})
		if response.GetStatus() != http.StatusUnauthorized || response.Headers().Get("WWW-Authenticate") == "" {
			t.Fatalf("非法凭据响应错误: values=%v status=%d headers=%v", values, response.GetStatus(), response.Headers())
		}
	}
}

// TestRequireDistinguishesUnauthenticatedAndForbidden 验证缺少主体返回 401、权限不足返回 403。
func TestRequireDistinguishesUnauthenticatedAndForbidden(t *testing.T) {
	rbac, _ := NewRBAC(map[string][]string{"reader": {"users.read"}})
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil))
	if response := Require(rbac, "users.read")(request, func(*fwcontext.Request) *fwcontext.Response { return nil }); response.GetStatus() != http.StatusUnauthorized {
		t.Fatalf("缺少主体应返回 401，实际为 %d", response.GetStatus())
	}
	identity, _ := NewIdentity("user", []string{"reader"}, nil, nil)
	request.Set(requestIdentityKey, identity)
	if response := Require(rbac, "users.write")(request, func(*fwcontext.Request) *fwcontext.Response { return nil }); response.GetStatus() != http.StatusForbidden {
		t.Fatalf("权限不足应返回 403，实际为 %d", response.GetStatus())
	}
}

// TestAuthenticationDependenciesRejectTypedNilAndInvalidIdentity 验证接口中的类型化 nil 与存储返回的非法主体均失败关闭。
func TestAuthenticationDependenciesRejectTypedNilAndInvalidIdentity(t *testing.T) {
	var nilStore *StaticTokenStore
	if _, err := Bearer(nilStore); err == nil {
		t.Fatal("类型化 nil token 存储必须被拒绝")
	}
	authenticator, err := Bearer(&invalidTokenStore{})
	if err != nil {
		t.Fatalf("创建测试认证器失败: %v", err)
	}
	raw := httptest.NewRequest(http.MethodGet, "/", nil)
	raw.Header.Set("Authorization", "Bearer token")
	response := Authenticate(authenticator)(fwcontext.MustNewRequest(raw), func(*fwcontext.Request) *fwcontext.Response {
		t.Fatal("非法主体不应进入下游")
		return nil
	})
	if response.GetStatus() != http.StatusUnauthorized {
		t.Fatalf("非法主体应返回 401，实际为 %d", response.GetStatus())
	}

	var nilRBAC *RBAC
	request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil))
	identity, _ := NewIdentity("user", nil, nil, nil)
	request.Set(requestIdentityKey, identity)
	if response := Require(nilRBAC, "users.read")(request, func(*fwcontext.Request) *fwcontext.Response { return nil }); response.GetStatus() != http.StatusForbidden {
		t.Fatalf("类型化 nil 授权器应返回 403，实际为 %d", response.GetStatus())
	}
}

// TestStaticTokenStoreValidatesAndFreezesIdentity 验证存储拒绝非法声明并隔离调用方后续修改。
func TestStaticTokenStoreValidatesAndFreezesIdentity(t *testing.T) {
	if _, err := NewStaticTokenStore(map[string]Identity{"token": {Subject: "user", Roles: []string{"bad\nrole"}}}); err == nil {
		t.Fatal("非法角色必须被拒绝")
	}
	roles := []string{"reader"}
	attributes := map[string]string{"tenant": "a"}
	store, err := NewStaticTokenStore(map[string]Identity{"token": {Subject: "user", Roles: roles, Attributes: attributes}})
	if err != nil {
		t.Fatalf("创建 token 存储失败: %v", err)
	}
	roles[0] = "admin"
	attributes["tenant"] = "changed"
	resolved, found, err := store.LookupToken(context.Background(), sha256.Sum256([]byte("token")))
	if err != nil || !found || resolved.Roles[0] != "reader" || resolved.Attributes["tenant"] != "a" {
		t.Fatalf("存储未冻结主体快照: identity=%#v found=%t err=%v", resolved, found, err)
	}
}
