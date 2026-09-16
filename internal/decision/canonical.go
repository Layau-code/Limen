package decision

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// CanonicalInput 使用固定结构顺序编码决策输入。
func CanonicalInput(input Input) ([]byte, error) {
	return json.Marshal(input)
}

// HashInput 计算规范化决策输入的内容哈希。
func HashInput(input Input) (string, error) {
	encoded, err := CanonicalInput(input)
	if err != nil {
		return "", err
	}
	return hashBytes(encoded), nil
}

// HashPlan 计算不包含自身哈希字段的执行计划内容哈希。
func HashPlan(plan ExecutionPlan) (string, error) {
	plan.PlanHash = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	return hashBytes(encoded), nil
}

// hashBytes 计算规范 JSON 的 SHA-256 内容标识。
func hashBytes(encoded []byte) string {
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}
