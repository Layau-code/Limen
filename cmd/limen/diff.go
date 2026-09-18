package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/configstore"
)

type diffOutput struct {
	Object           string               `json:"object"`
	BaseVersion      string               `json:"base_version"`
	CandidateVersion string               `json:"candidate_version"`
	Changed          bool                 `json:"changed"`
	Changes          []configstore.Change `json:"changes"`
}

// runDiff 比较两个本地模型目录，输出不含配置值的发布影响摘要。
func runDiff(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("diff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	basePath := flags.String("base", "", "当前模型目录 JSON 文件路径")
	candidatePath := flags.String("candidate", "", "候选模型目录 JSON 文件路径")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("diff 不接受位置参数")
	}
	if *basePath == "" || *candidatePath == "" {
		return errors.New("diff 需要 --base 和 --candidate")
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
	output := diffOutput{
		Object:           "config_diff",
		BaseVersion:      base.Version,
		CandidateVersion: candidate.Version,
		Changed:          len(changes) > 0,
		Changes:          changes,
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(output)
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
