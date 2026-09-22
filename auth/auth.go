package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
)

const (
	maximumBearerTokenBytes = 8192
	requestIdentityKey      = "__thinkgo.auth.identity"
)

var (
	ErrUnauthenticated   = errors.New("unauthenticated")
	ErrForbidden         = errors.New("forbidden")
	ErrInvalidIdentity   = errors.New("invalid identity")
	ErrIdentityCollision = errors.New("identity normalization collision")
)

// Identity 是认证成功后写入当前请求的不可变主体快照。
type Identity struct {
	Subject     string
	Roles       []string
	Permissions []string
	Attributes  map[string]string
}

// NewIdentity 校验主体并复制所有可变字段。
func NewIdentity(subject string, roles, permissions []string, attributes map[string]string) (Identity, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" || len(subject) > 512 || strings.ContainsAny(subject, "\r\n\x00") {
		return Identity{}, ErrInvalidIdentity
	}
	identity := Identity{Subject: subject}
	var err error
	if identity.Roles, err = normalizedClaims(roles); err != nil {
		return Identity{}, err
	}
	if identity.Permissions, err = normalizedClaims(permissions); err != nil {
		return Identity{}, err
	}
	identity.Attributes = make(map[string]string, len(attributes))
	attributeSources := make(map[string]string, len(attributes))
	for rawKey, value := range attributes {
		key := strings.TrimSpace(rawKey)
		if key == "" || len(key) > 128 || len(value) > 4096 || strings.ContainsAny(key+value, "\r\n\x00") {
			return Identity{}, ErrInvalidIdentity
		}
		if previous, exists := attributeSources[key]; exists && previous != rawKey {
			return Identity{}, identityCollisionError()
		}
		attributeSources[key] = rawKey
		identity.Attributes[key] = value
	}
	return identity, nil
}

func normalizedClaims(values []string) ([]string, error) {
	seen := make(map[string]string, len(values))
	result := make([]string, 0, len(values))
	for _, rawValue := range values {
		value := strings.TrimSpace(rawValue)
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			return nil, ErrInvalidIdentity
		}
		if previous, exists := seen[value]; exists {
			if previous != rawValue {
				return nil, identityCollisionError()
			}
			continue
		}
		seen[value] = rawValue
		result = append(result, value)
	}
	return result, nil
}

// identityCollisionError 同时保留通用身份错误和规范化冲突错误，便于调用方稳定分类。
func identityCollisionError() error {
	return errors.Join(ErrInvalidIdentity, ErrIdentityCollision)
}

// Authenticator 根据当前请求返回主体，认证失败使用 ErrUnauthenticated。
type Authenticator interface {
	Authenticate(context.Context, *fwcontext.Request) (Identity, error)
}

// Authorizer 根据已认证主体和权限返回授权结果。
type Authorizer interface {
	Authorize(context.Context, Identity, string) error
}

// TokenStore 只接收 SHA-256 token 摘要，存储实现不需要持有明文凭据。
type TokenStore interface {
	LookupToken(context.Context, [sha256.Size]byte) (Identity, bool, error)
}

// StaticTokenStore 是并发安全、只保存 token 摘要的进程内凭据存储。
type StaticTokenStore struct {
	mu     sync.RWMutex
	tokens map[[sha256.Size]byte]Identity
}

// NewStaticTokenStore 从启动期凭据快照创建存储；空 token 和非法主体会失败关闭。
func NewStaticTokenStore(tokens map[string]Identity) (*StaticTokenStore, error) {
	store := &StaticTokenStore{tokens: make(map[[sha256.Size]byte]Identity, len(tokens))}
	for token, identity := range tokens {
		if token == "" || len(token) > maximumBearerTokenBytes || strings.ContainsAny(token, "\r\n\x00") {
			return nil, ErrInvalidIdentity
		}
		validated, err := NewIdentity(identity.Subject, identity.Roles, identity.Permissions, identity.Attributes)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256([]byte(token))
		if _, duplicate := store.tokens[digest]; duplicate {
			return nil, errors.New("duplicate bearer token digest")
		}
		store.tokens[digest] = validated
	}
	return store, nil
}

func (store *StaticTokenStore) LookupToken(_ context.Context, digest [sha256.Size]byte) (Identity, bool, error) {
	if store == nil {
		return Identity{}, false, ErrUnauthenticated
	}
	store.mu.RLock()
	identity, exists := store.tokens[digest]
	store.mu.RUnlock()
	return cloneIdentity(identity), exists, nil
}

type bearerAuthenticator struct{ store TokenStore }

// Bearer 创建严格拒绝重复 Authorization、错误 scheme 和超长 token 的认证器。
func Bearer(store TokenStore) (Authenticator, error) {
	if isNilDependency(store) {
		return nil, ErrUnauthenticated
	}
	return &bearerAuthenticator{store: store}, nil
}

func (authenticator *bearerAuthenticator) Authenticate(ctx context.Context, request *fwcontext.Request) (Identity, error) {
	if request == nil || request.Raw() == nil || ctx == nil {
		return Identity{}, ErrUnauthenticated
	}
	values := request.Raw().Header.Values("Authorization")
	if len(values) != 1 {
		return Identity{}, ErrUnauthenticated
	}
	scheme, token, exists := strings.Cut(values[0], " ")
	if !exists || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.TrimSpace(token) != token || len(token) > maximumBearerTokenBytes {
		return Identity{}, ErrUnauthenticated
	}
	digest := sha256.Sum256([]byte(token))
	identity, found, err := authenticator.store.LookupToken(ctx, digest)
	if err != nil {
		return Identity{}, err
	}
	if !found {
		return Identity{}, ErrUnauthenticated
	}
	validated, err := NewIdentity(identity.Subject, identity.Roles, identity.Permissions, identity.Attributes)
	if err != nil {
		return Identity{}, ErrUnauthenticated
	}
	return validated, nil
}

// Authenticate 返回把主体写入当前请求的认证中间件。
func Authenticate(authenticator Authenticator) middleware.Handler {
	return func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		if isNilDependency(authenticator) || request == nil || next == nil {
			return unauthorizedResponse()
		}
		identity, err := authenticator.Authenticate(request.Context(), request)
		if err != nil {
			if errors.Is(err, ErrUnauthenticated) {
				return unauthorizedResponse()
			}
			// 基础设施错误交给统一异常链记录与渲染，不能伪装成凭据无效。
			panic(err)
		}
		request.Set(requestIdentityKey, cloneIdentity(identity))
		return next(request)
	}
}

// Require 返回要求指定权限的授权中间件。
func Require(authorizer Authorizer, permission string) middleware.Handler {
	permission = strings.TrimSpace(permission)
	return func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		identity, exists := IdentityFromRequest(request)
		if !exists {
			return unauthorizedResponse()
		}
		if isNilDependency(authorizer) || permission == "" {
			return fwcontext.NewResponse().Code(http.StatusForbidden).Content(http.StatusText(http.StatusForbidden))
		}
		if err := authorizer.Authorize(request.Context(), identity, permission); err != nil {
			if errors.Is(err, ErrForbidden) {
				return fwcontext.NewResponse().Code(http.StatusForbidden).Content(http.StatusText(http.StatusForbidden))
			}
			panic(err)
		}
		if next == nil {
			return fwcontext.NewResponse().Code(http.StatusInternalServerError).Content(http.StatusText(http.StatusInternalServerError))
		}
		return next(request)
	}
}

// IdentityFromRequest 返回防御性复制的当前认证主体。
func IdentityFromRequest(request *fwcontext.Request) (Identity, bool) {
	if request == nil {
		return Identity{}, false
	}
	value := request.GetData(requestIdentityKey)
	identity, ok := value.(Identity)
	return cloneIdentity(identity), ok && identity.Subject != ""
}

// RBAC 根据权限和角色到权限的映射执行精确匹配授权。
type RBAC struct {
	rolePermissions map[string]map[string]struct{}
}

// NewRBAC 校验并冻结角色权限映射。
func NewRBAC(roles map[string][]string) (*RBAC, error) {
	rbac := &RBAC{rolePermissions: make(map[string]map[string]struct{}, len(roles))}
	roleSources := make(map[string]string, len(roles))
	for role, permissions := range roles {
		normalizedRole, err := normalizedClaims([]string{role})
		if err != nil {
			return nil, err
		}
		if previous, exists := roleSources[normalizedRole[0]]; exists && previous != role {
			return nil, identityCollisionError()
		}
		roleSources[normalizedRole[0]] = role
		normalizedPermissions, err := normalizedClaims(permissions)
		if err != nil {
			return nil, err
		}
		set := make(map[string]struct{}, len(normalizedPermissions))
		for _, permission := range normalizedPermissions {
			set[permission] = struct{}{}
		}
		rbac.rolePermissions[normalizedRole[0]] = set
	}
	return rbac, nil
}

func (rbac *RBAC) Authorize(_ context.Context, identity Identity, permission string) error {
	permission = strings.TrimSpace(permission)
	if rbac == nil || permission == "" {
		return ErrForbidden
	}
	for _, granted := range identity.Permissions {
		if granted == permission {
			return nil
		}
	}
	for _, role := range identity.Roles {
		if _, granted := rbac.rolePermissions[role][permission]; granted {
			return nil
		}
	}
	return ErrForbidden
}

func unauthorizedResponse() *fwcontext.Response {
	return fwcontext.NewResponse().Header("WWW-Authenticate", `Bearer realm="api"`).Code(http.StatusUnauthorized).Content(http.StatusText(http.StatusUnauthorized))
}

func cloneIdentity(identity Identity) Identity {
	identity.Roles = append([]string(nil), identity.Roles...)
	identity.Permissions = append([]string(nil), identity.Permissions...)
	attributes := make(map[string]string, len(identity.Attributes))
	for key, value := range identity.Attributes {
		attributes[key] = value
	}
	identity.Attributes = attributes
	return identity
}

func isNilDependency(value interface{}) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
