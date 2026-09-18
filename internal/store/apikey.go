package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/huz/limen/internal/auth"
)

// APIKeyAuthenticator 从 PostgreSQL 读取租户 API Key 的摘要和 Scope。
type APIKeyAuthenticator struct {
	db     *sql.DB
	secret []byte
}

// NewAPIKeyAuthenticator 创建使用 HMAC 摘要校验数据库 Key 的鉴权器。
func NewAPIKeyAuthenticator(db *sql.DB, hmacSecret string) *APIKeyAuthenticator {
	return &APIKeyAuthenticator{db: db, secret: []byte(hmacSecret)}
}

// AuthenticateContext 解析高熵 Key，并只返回对应租户 Principal。
func (authenticator *APIKeyAuthenticator) AuthenticateContext(ctx context.Context, header string) (auth.Principal, bool, error) {
	if authenticator == nil || authenticator.db == nil || len(authenticator.secret) == 0 {
		return auth.Principal{}, false, errors.New("api key store is unavailable")
	}
	prefix, token, ok := splitAPIKey(header)
	if !ok {
		return auth.Principal{}, false, nil
	}
	var tenantID string
	var expectedDigest []byte
	var scopesJSON []byte
	var active bool
	var expiresAt sql.NullTime
	err := authenticator.db.QueryRowContext(ctx, `SELECT tenant_id,digest,scopes,active,expires_at FROM public.limen_lookup_api_key($1)`, prefix).Scan(&tenantID, &expectedDigest, &scopesJSON, &active, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Principal{}, false, nil
	}
	if err != nil {
		return auth.Principal{}, false, err
	}
	digest := hmacDigest(authenticator.secret, token)
	if !active || len(expectedDigest) != len(digest) || subtle.ConstantTimeCompare(expectedDigest, digest) != 1 || expiresAt.Valid && !time.Now().Before(expiresAt.Time) {
		return auth.Principal{}, false, nil
	}
	var scopes []auth.Scope
	if err := json.Unmarshal(scopesJSON, &scopes); err != nil {
		return auth.Principal{}, false, err
	}
	set := make(map[auth.Scope]struct{}, len(scopes))
	for _, scope := range scopes {
		if !auth.IsKnownScope(scope) {
			return auth.Principal{}, false, errors.New("api key contains unknown scope")
		}
		set[scope] = struct{}{}
	}
	return auth.Principal{TenantID: tenantID, Scopes: set}, true, nil
}

// splitAPIKey 提取公开前缀，同时拒绝低熵或非 Limen Key 格式。
func splitAPIKey(header string) (string, string, bool) {
	token, found := strings.CutPrefix(header, "Bearer ")
	if !found || !strings.HasPrefix(token, "lmn_live_") {
		return "", "", false
	}
	rest := strings.TrimPrefix(token, "lmn_live_")
	separator := strings.LastIndexByte(rest, '_')
	if separator <= 0 || separator == len(rest)-1 {
		return "", "", false
	}
	prefix := rest[:separator]
	if !auth.ValidatePublicPrefix(prefix) {
		return "", "", false
	}
	return prefix, token, true
}

// hmacDigest 计算数据库保存的 API Key HMAC-SHA-256 摘要。
func hmacDigest(secret []byte, token string) []byte {
	h := hmac.New(sha256.New, secret)
	_, _ = h.Write([]byte(token))
	return h.Sum(nil)
}

var _ auth.Authenticator = (*APIKeyAuthenticator)(nil)
