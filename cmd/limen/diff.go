package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/configstore"
)

type diffOutput struct {
	Object           string               `json:"object"`
	BaseVersion      string               `json:"base_version"`
	CandidateVersion string               `json:"candidate_version"`
	Changed          bool                 `json:"changed"`
	Changes          []configstore.Change `json:"changes"`
	Blocked          bool                 `json:"blocked"`
	Violations       []diffViolation      `json:"violations"`
}

type diffViolation struct {
	Category string `json:"category"`
	Path     string `json:"path"`
	Kind     string `json:"kind"`
}

type diffBlockedError struct {
	Count int
}

// Error 返回不含配置值的门禁失败提示。
func (err *diffBlockedError) Error() string {
	return fmt.Sprintf("diff 被策略阻止: %d 项变化命中门禁", err.Count)
}

// runDiff 比较两个本地模型目录，输出不含配置值的发布影响摘要。
func runDiff(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("diff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	basePath := flags.String("base", "", "当前模型目录 JSON 文件路径")
	candidatePath := flags.String("candidate", "", "候选模型目录 JSON 文件路径")
	failOn := flags.String("fail-on", "", "命中指定变化类别时返回失败（逗号分隔：model,target,provider,endpoint,policy,routing,any）")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("diff 不接受位置参数")
	}
	if *basePath == "" || *candidatePath == "" {
		return errors.New("diff 需要 --base 和 --candidate")
	}
	failCategories, err := parseDiffCategories(*failOn)
	if err != nil {
		return err
	}
	base, err := readConfigRecord(*basePath)
	if err != nil {
		return fmt.Errorf("读取基线配置失败: %w", err)
	}
	candidate, err := readConfigRecord(*candidatePath)
	if err != nil {
		return fmt.Errorf("读取候选配置失败: %w", err)
	}
	changes := configstore.PublicDiff(base, candidate)
	violations := make([]diffViolation, 0)
	for _, change := range changes {
		category := classifyDiff(change.Path)
		if diffCategoryMatches(failCategories, category) {
			violations = append(violations, diffViolation{Category: category, Path: change.Path, Kind: change.Kind})
		}
	}
	output := diffOutput{
		Object:           "config_diff",
		BaseVersion:      base.Version,
		CandidateVersion: candidate.Version,
		Changed:          len(changes) > 0,
		Changes:          changes,
		Blocked:          len(violations) > 0,
		Violations:       violations,
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(output); err != nil {
		return err
	}
	if len(violations) > 0 {
		return &diffBlockedError{Count: len(violations)}
	}
	return nil
}

// parseDiffCategories 解析并校验发布门禁允许的变化类别。
func parseDiffCategories(raw string) (map[string]struct{}, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	allowed := map[string]struct{}{
		"model": {}, "target": {}, "provider": {}, "endpoint": {}, "policy": {}, "routing": {}, "any": {},
	}
	result := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		category := strings.TrimSpace(part)
		if _, ok := allowed[category]; !ok {
			return nil, errors.New("diff --fail-on 包含不支持的类别")
		}
		if _, exists := result[category]; exists {
			return nil, errors.New("diff --fail-on 包含重复类别")
		}
		result[category] = struct{}{}
	}
	return result, nil
}

// classifyDiff 将安全 diff 路径归并为稳定的发布风险类别。
func classifyDiff(path string) string {
	if path == "routing" {
		return "routing"
	}
	if !strings.Contains(path, ".targets[") {
		return "model"
	}
	switch {
	case strings.HasSuffix(path, ".provider"), strings.HasSuffix(path, ".upstream_model"):
		return "provider"
	case strings.HasSuffix(path, ".endpoint_id"):
		return "endpoint"
	case strings.HasSuffix(path, ".policy"), strings.HasSuffix(path, ".capabilities"):
		return "policy"
	default:
		return "target"
	}
}

// diffCategoryMatches 判断变化类别是否命中门禁声明。
func diffCategoryMatches(categories map[string]struct{}, actual string) bool {
	if len(categories) == 0 {
		return false
	}
	if _, ok := categories["any"]; ok {
		return true
	}
	if _, ok := categories[actual]; ok {
		return true
	}
	if _, ok := categories["target"]; ok {
		return actual == "provider" || actual == "endpoint" || actual == "policy" || actual == "target"
	}
	return false
}

// readConfigRecord 严格解析本地配置并构造只用于 diff 的不可变记录。
func readConfigRecord(path string) (configstore.Record, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return configstore.Record{}, err
	}
	models, routing, version, err := config.ParseModels(contents, config.DefaultRouting())
	if err != nil {
		return configstore.Record{}, err
	}
	return configstore.Record{Version: version, Models: models, Routing: routing}, nil
}
