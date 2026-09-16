package run

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewID 生成不包含业务正文的随机资源 ID。
func NewID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate resource id: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(value[:]), nil
}
