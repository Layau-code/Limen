// Package credentialstore 保存经过加密的 Provider 凭据，并绑定租户和 endpoint。
package credentialstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNotFound 表示指定租户、Provider 或 endpoint 没有有效凭据。
	ErrNotFound = errors.New("provider credential not found")
	// ErrInvalidCredential 表示凭据或绑定信息不满足安全边界。
	ErrInvalidCredential = errors.New("invalid provider credential")
)

// Record 是不包含明文凭据的可审计元数据。
type Record struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"-"`
	Provider   string     `json:"provider"`
	EndpointID string     `json:"endpoint_id"`
	KeyVersion string     `json:"key_version"`
	Active     bool       `json:"active"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Store 定义凭据轮换、解析和撤销边界。
type Store interface {
	Rotate(context.Context, string, string, string, string) (Record, error)
	Resolve(context.Context, string, string, string) ([]byte, Record, error)
	Revoke(context.Context, string, string, string) error
}

// Vault 使用 AES-GCM 加密凭据，并用绑定信息作为附加认证数据。
type Vault struct {
	key []byte
}

// NewVault 创建要求 32 字节主密钥的凭据加密器。
func NewVault(masterKey []byte) (*Vault, error) {
	if len(masterKey) != 32 {
		return nil, errors.New("credential master key must be 32 bytes")
	}
	return &Vault{key: append([]byte(nil), masterKey...)}, nil
}

// ParseMasterKey 将部署环境中的十六进制、Base64 或原始 32 字节主密钥解析为密钥。
func ParseMasterKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("credential master key is empty")
	}
	if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if len(raw) == 32 {
		return []byte(raw), nil
	}
	return nil, errors.New("credential master key must encode 32 bytes")
}

// Encrypt 加密凭据，密文中包含随机 Nonce 但不包含主密钥。
func (vault *Vault) Encrypt(tenantID, provider, endpointID, secret string) ([]byte, error) {
	if vault == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(provider) == "" || strings.TrimSpace(endpointID) == "" || secret == "" {
		return nil, ErrInvalidCredential
	}
	block, err := aes.NewCipher(vault.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(secret), associatedData(tenantID, provider, endpointID)), nil
}

// Decrypt 解密并校验租户、Provider 和 endpoint 绑定信息。
func (vault *Vault) Decrypt(tenantID, provider, endpointID string, ciphertext []byte) ([]byte, error) {
	if vault == nil || len(ciphertext) == 0 {
		return nil, ErrInvalidCredential
	}
	block, err := aes.NewCipher(vault.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, ErrInvalidCredential
	}
	return gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], associatedData(tenantID, provider, endpointID))
}

// MemoryStore 是单进程开发和测试使用的加密凭据存储。
type MemoryStore struct {
	mu      sync.RWMutex
	vault   *Vault
	now     func() time.Time
	entries map[string]memoryEntry
	active  map[string]string
}

type memoryEntry struct {
	record Record
	sealed []byte
}

// NewMemoryStore 创建使用指定主密钥的内存凭据存储。
func NewMemoryStore(vault *Vault) *MemoryStore {
	return &MemoryStore{vault: vault, now: time.Now, entries: make(map[string]memoryEntry), active: make(map[string]string)}
}

// Rotate 加密并替换一个租户 Provider endpoint 的当前凭据。
func (store *MemoryStore) Rotate(ctx context.Context, tenantID, provider, endpointID, secret string) (Record, error) {
	if err := contextError(ctx); err != nil {
		return Record{}, err
	}
	if store == nil || store.vault == nil {
		return Record{}, ErrInvalidCredential
	}
	sealed, err := store.vault.Encrypt(tenantID, provider, endpointID, secret)
	if err != nil {
		return Record{}, err
	}
	now := store.now().UTC()
	id := credentialID(sealed, now)
	record := Record{ID: id, TenantID: tenantID, Provider: provider, EndpointID: endpointID, KeyVersion: id, Active: true, CreatedAt: now}
	key := activeKey(tenantID, provider, endpointID)
	store.mu.Lock()
	defer store.mu.Unlock()
	if oldID := store.active[key]; oldID != "" {
		old := store.entries[oldID]
		old.record.Active = false
		old.record.RevokedAt = &now
		store.entries[oldID] = old
	}
	store.entries[id] = memoryEntry{record: record, sealed: append([]byte(nil), sealed...)}
	store.active[key] = id
	return record, nil
}

// Resolve 解密当前有效凭据，调用方不得持久化返回的明文。
func (store *MemoryStore) Resolve(ctx context.Context, tenantID, provider, endpointID string) ([]byte, Record, error) {
	if err := contextError(ctx); err != nil {
		return nil, Record{}, err
	}
	if store == nil || store.vault == nil {
		return nil, Record{}, ErrInvalidCredential
	}
	store.mu.RLock()
	id := store.active[activeKey(tenantID, provider, endpointID)]
	entry, ok := store.entries[id]
	store.mu.RUnlock()
	if !ok || !entry.record.Active {
		return nil, Record{}, ErrNotFound
	}
	secret, err := store.vault.Decrypt(tenantID, provider, endpointID, entry.sealed)
	if err != nil {
		return nil, Record{}, err
	}
	return secret, entry.record, nil
}

// Revoke 使指定 endpoint 的当前凭据立即失效。
func (store *MemoryStore) Revoke(ctx context.Context, tenantID, provider, endpointID string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if store == nil {
		return ErrInvalidCredential
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key := activeKey(tenantID, provider, endpointID)
	id := store.active[key]
	entry, ok := store.entries[id]
	if !ok || !entry.record.Active {
		return ErrNotFound
	}
	now := store.now().UTC()
	entry.record.Active = false
	entry.record.RevokedAt = &now
	store.entries[id] = entry
	delete(store.active, key)
	return nil
}

// associatedData 生成不能被跨租户或跨 endpoint 重放的认证上下文。
func associatedData(tenantID, provider, endpointID string) []byte {
	return []byte(tenantID + "\x00" + provider + "\x00" + endpointID)
}

// activeKey 组合租户、Provider 和 endpoint，定位当前有效凭据。
func activeKey(tenantID, provider, endpointID string) string {
	return tenantID + "\x00" + provider + "\x00" + endpointID
}

// credentialID 根据密文和时间生成不包含明文的凭据摘要标识。
func credentialID(sealed []byte, now time.Time) string {
	sum := sha256.Sum256(append(sealed, []byte(now.UTC().Format(time.RFC3339Nano))...))
	return "cred_" + hex.EncodeToString(sum[:12])
}

// NewID 生成不包含凭据内容的凭据记录标识。
func NewID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "cred_" + hex.EncodeToString(bytes), nil
}

// contextError 保持内存凭据存储与数据库存储一致的取消语义。
func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
