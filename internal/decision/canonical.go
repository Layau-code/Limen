package decision

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// CanonicalInput 规范化无序集合后使用固定结构顺序编码决策输入。
func CanonicalInput(input Input) ([]byte, error) {
	if input.AlgorithmVersion == AlgorithmVersionV2 {
		input = canonicalizeInput(input)
	}
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
	if plan.AlgorithmVersion == AlgorithmVersionV2 {
		plan = canonicalizePlan(plan)
	}
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

// canonicalizeInput 将能力和数据等级集合排序去重，避免集合顺序影响快照哈希。
func canonicalizeInput(input Input) Input {
	input.Request.Contract.RequiredCapabilities = sortedUnique(input.Request.Contract.RequiredCapabilities)
	for index := range input.Candidates {
		candidate := &input.Candidates[index]
		candidate.Target.Capabilities = sortedUnique(candidate.Target.Capabilities)
		candidate.Target.DataClasses = sortedUnique(candidate.Target.DataClasses)
	}
	if input.Request.Model == "auto" || input.Request.Contract.Active {
		sort.SliceStable(input.Candidates, func(left, right int) bool {
			leftKey := input.Candidates[left].ModelID + "\x00" + input.Candidates[left].Target.ID
			rightKey := input.Candidates[right].ModelID + "\x00" + input.Candidates[right].Target.ID
			return leftKey < rightKey
		})
	}
	return input
}

// canonicalizePlan 规范化计划中的集合字段，但保留目标优先级顺序。
func canonicalizePlan(plan ExecutionPlan) ExecutionPlan {
	plan.Targets = append([]PlanTarget(nil), plan.Targets...)
	for index := range plan.Targets {
		plan.Targets[index].Target.Capabilities = sortedUnique(plan.Targets[index].Target.Capabilities)
		plan.Targets[index].Target.DataClasses = sortedUnique(plan.Targets[index].Target.DataClasses)
	}
	return plan
}

// sortedUnique 返回排序且去重后的字符串集合副本。
func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] == result[write-1] {
			continue
		}
		result[write] = result[read]
		write++
	}
	return result[:write]
}
