package run

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// ErrIdempotencyKeyRequired 表示受治理请求缺少幂等键。
var ErrIdempotencyKeyRequired = errors.New("idempotency key is required")

// ErrIdempotencyConflict 表示同一幂等键对应了不同请求哈希。
var ErrIdempotencyConflict = errors.New("idempotency key conflict")

// ErrRequestInProgress 表示同一幂等请求仍在执行或等待结算。
var ErrRequestInProgress = errors.New("request is in progress")

// ErrRequestAlreadyProcessed 表示同一幂等请求已经完成处理。
var ErrRequestAlreadyProcessed = errors.New("request already processed")

type canonicalHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type canonicalRequest struct {
	TenantID string            `json:"tenant_id"`
	Endpoint string            `json:"endpoint"`
	Key      string            `json:"idempotency_key"`
	Body     string            `json:"body_base64"`
	Headers  []canonicalHeader `json:"headers"`
}

// HashRequest 为请求正文和影响路由的 Header 生成稳定幂等哈希。
func HashRequest(tenantID, endpoint, key string, body []byte, limenHeaders map[string]string) (string, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(endpoint) == "" || strings.TrimSpace(key) == "" {
		return "", ErrIdempotencyKeyRequired
	}
	headerNames := make([]string, 0, len(limenHeaders))
	for name := range limenHeaders {
		if strings.EqualFold(name, "authorization") {
			continue
		}
		headerNames = append(headerNames, strings.ToLower(strings.TrimSpace(name)))
	}
	sort.Strings(headerNames)
	headers := make([]canonicalHeader, 0, len(headerNames))
	for _, name := range headerNames {
		for sourceName, value := range limenHeaders {
			if strings.EqualFold(strings.TrimSpace(sourceName), name) {
				headers = append(headers, canonicalHeader{Name: name, Value: value})
				break
			}
		}
	}
	encoded, err := json.Marshal(canonicalRequest{
		TenantID: tenantID,
		Endpoint: endpoint,
		Key:      key,
		Body:     base64.StdEncoding.EncodeToString(body),
		Headers:  headers,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
