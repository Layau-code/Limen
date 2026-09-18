// Package auth 定义 Limen 鉴权后的租户 Principal 和 Scope 边界。
package auth

import (
	"context"
	"crypto/subtle"
	"strings"
)

// Scope 控制一个 Principal 可以访问的 API 能力。
type Scope string

const (
	ScopeInference    Scope = "inference"
	ScopeRunsRead     Scope = "runs:read"
	ScopeRunsWrite    Scope = "runs:write"
	ScopeDecisions    Scope = "decisions:read"
	ScopeConfigsRead  Scope = "configs:read"
	ScopeConfigsWrite Scope = "configs:write"
	ScopeAdmin        Scope = "admin"
)

// Principal 表示已通过鉴权的租户身份，不暴露原始 API Key；Subject 是非敏感的凭据身份标识。
type Principal struct {
	TenantID string
	Subject  string
	Scopes   map[Scope]struct{}
}

// Authenticator 将外部凭据解析为租户 Principal，下游不接触原始 Key。
type Authenticator interface {
	AuthenticateContext(context.Context, string) (Principal, bool, error)
}

// HasScope 判断 Principal 是否拥有指定 Scope 或管理员权限。
func (principal Principal) HasScope(scope Scope) bool {
	if _, ok := principal.Scopes[ScopeAdmin]; ok {
		return true
	}
	_, ok := principal.Scopes[scope]
	return ok
}

// StaticAuthenticator 是单机环境变量 API Key 的鉴权实现。
type StaticAuthenticator struct {
	key       []byte
	principal Principal
}

// NewStaticAuthenticator 创建不持久化 API Key 的单机鉴权器。
func NewStaticAuthenticator(apiKey, tenantID string, scopes []Scope) StaticAuthenticator {
	if len(scopes) == 0 {
		scopes = AllScopes()
	}
	set := make(map[Scope]struct{}, len(scopes))
	for _, scope := range scopes {
		set[scope] = struct{}{}
	}
	return StaticAuthenticator{key: []byte(apiKey), principal: Principal{TenantID: tenantID, Subject: "static", Scopes: set}}
}

// Authenticate 校验 Bearer Key，并用常量时间比较避免时序泄露。
func (authenticator StaticAuthenticator) Authenticate(header string) (Principal, bool) {
	token, found := strings.CutPrefix(header, "Bearer ")
	if !found || len(authenticator.key) == 0 || len(token) != len(authenticator.key) || subtle.ConstantTimeCompare([]byte(token), authenticator.key) != 1 {
		return Principal{}, false
	}
	scopes := make(map[Scope]struct{}, len(authenticator.principal.Scopes))
	for scope := range authenticator.principal.Scopes {
		scopes[scope] = struct{}{}
	}
	return Principal{TenantID: authenticator.principal.TenantID, Subject: authenticator.principal.Subject, Scopes: scopes}, true
}

// AuthenticateContext 适配统一鉴权接口，静态 Key 不访问外部 Store。
func (authenticator StaticAuthenticator) AuthenticateContext(_ context.Context, header string) (Principal, bool, error) {
	principal, ok := authenticator.Authenticate(header)
	return principal, ok, nil
}

// AllScopes 返回单机开发模式的完整 Scope 集合副本。
func AllScopes() []Scope {
	return []Scope{ScopeInference, ScopeRunsRead, ScopeRunsWrite, ScopeDecisions, ScopeConfigsRead, ScopeConfigsWrite, ScopeAdmin}
}

// IsKnownScope 判断 Scope 是否属于 Limen 固定权限集合。
func IsKnownScope(candidate Scope) bool {
	for _, scope := range AllScopes() {
		if candidate == scope {
			return true
		}
	}
	return false
}
