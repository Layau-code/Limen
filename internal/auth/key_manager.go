package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

var (
	// ErrAPIKeyNotFound 表示租户下没有对应的 API Key。
	ErrAPIKeyNotFound = errors.New("api key not found")
	// ErrAPIKeyConflict 表示幂等键对应了不同的 API Key 操作。
	ErrAPIKeyConflict = errors.New("api key operation conflict")
	// ErrInvalidAPIKeyOperation 表示 API Key 操作缺少必要的安全字段。
	ErrInvalidAPIKeyOperation = errors.New("invalid api key operation")
)

// APIKeyRecord 是不包含明文 Key 的租户凭据元数据。
type APIKeyRecord struct {
	PublicPrefix string     `json:"public_prefix"`
	TenantID     string     `json:"-"`
	Scopes       []Scope    `json:"scopes"`
	Active       bool       `json:"active"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// APIKeyMutation 保存 API Key 控制面的幂等键和规范请求哈希。
type APIKeyMutation struct {
	Key  string
	Hash string
}

// APIKeyManager 定义租户 API Key 的创建、查询和撤销边界。
type APIKeyManager interface {
	Create(context.Context, string, []Scope, *time.Time, APIKeyMutation) (APIKeyRecord, string, error)
	List(context.Context, string) ([]APIKeyRecord, error)
	Revoke(context.Context, string, string, APIKeyMutation) error
}

// ValidateAPIKeyScopes 校验 Key 可以携带的固定 Scope 集合。
func ValidateAPIKeyScopes(scopes []Scope) error {
	if len(scopes) == 0 {
		return ErrInvalidAPIKeyOperation
	}
	seen := make(map[Scope]struct{}, len(scopes))
	for _, scope := range scopes {
		if strings.TrimSpace(string(scope)) == "" || !IsKnownScope(scope) {
			return ErrInvalidAPIKeyOperation
		}
		if _, exists := seen[scope]; exists {
			return ErrInvalidAPIKeyOperation
		}
		seen[scope] = struct{}{}
	}
	return nil
}

// ValidatePublicPrefix 检查公开前缀格式，避免动态路径和数据库查询接收任意文本。
func ValidatePublicPrefix(prefix string) bool {
	if len(prefix) != 16 {
		return false
	}
	_, err := hex.DecodeString(prefix)
	return err == nil
}
