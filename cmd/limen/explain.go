package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/huz/limen/internal/catalog"
	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/httpapi"
)

type explainCandidate struct {
	ModelID  string `json:"model_id"`
	TargetID string `json:"target_id"`
	Provider string `json:"provider"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason"`
}

type explainTarget struct {
	ModelID  string `json:"model_id"`
	TargetID string `json:"target_id"`
	Provider string `json:"provider"`
}

type explainOutput struct {
	Model             string             `json:"model"`
	ConfigVersion     string             `json:"config_version"`
	AlgorithmVersion  string             `json:"algorithm_version"`
	DecisionError     string             `json:"decision_error,omitempty"`
	EffectiveStrategy string             `json:"effective_strategy"`
	Reasons           []string           `json:"reasons,omitempty"`
	Candidates        []explainCandidate `json:"candidates"`
	Targets           []explainTarget    `json:"targets"`
	InputHash         string             `json:"input_hash"`
	PlanHash          string             `json:"plan_hash"`
}

// runExplain 从本地文件生成不访问 Provider 的可复现路由解释。
func runExplain(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("explain", flag.ContinueOnError)
	flags.SetOutput(stderr)
	modelsPath := flags.String("models", "", "模型目录 JSON 文件路径")
	requestPath := flags.String("request", "", "OpenAI Chat 请求 JSON 文件路径")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("explain 不接受位置参数")
	}
	if *modelsPath == "" || *requestPath == "" {
		return errors.New("explain 需要 --models 和 --request")
	}
	modelBytes, err := os.ReadFile(*modelsPath)
	if err != nil {
		return fmt.Errorf("读取模型目录失败: %w", err)
	}
	models, routing, configVersion, err := config.ParseModels(modelBytes, config.DefaultRouting())
	if err != nil {
		return fmt.Errorf("解析模型目录失败: %w", err)
	}
	requestBytes, err := os.ReadFile(*requestPath)
	if err != nil {
		return fmt.Errorf("读取请求文件失败: %w", err)
	}
	request, contract, err := httpapi.ParseChatRequest(requestBytes)
	if err != nil {
		return fmt.Errorf("解析请求失败: %w", err)
	}
	registry, err := registryFromConfig(models)
	if err != nil {
		return fmt.Errorf("创建模型目录失败: %w", err)
	}
	router := gateway.NewRouter(nil, registry, gateway.Policy{
		RequestTimeout:   time.Minute,
		AttemptTimeout:   routing.AttemptTimeout,
		FailureThreshold: routing.FailureThreshold,
		Cooldown:         routing.Cooldown,
	})
	router.SetConfigVersion(configVersion)
	input, plan, err := router.ExplainAt(time.Unix(0, 0), request, contract)
	decisionError := ""
	if err != nil {
		var typedError *decision.DecisionError
		if !errors.As(err, &typedError) || typedError.Code != "no_eligible_target" {
			return fmt.Errorf("生成路由计划失败: %w", err)
		}
		decisionError = typedError.Code
	}
	output := explainOutput{
		Model:             request.Model,
		ConfigVersion:     plan.ConfigVersion,
		AlgorithmVersion:  plan.AlgorithmVersion,
		DecisionError:     decisionError,
		EffectiveStrategy: plan.EffectiveStrategy,
		Reasons:           append([]string(nil), plan.Reasons...),
		InputHash:         plan.InputHash,
		PlanHash:          plan.PlanHash,
		Candidates:        make([]explainCandidate, 0, len(plan.Candidates)),
		Targets:           make([]explainTarget, 0, len(plan.Targets)),
	}
	for _, candidate := range plan.Candidates {
		output.Candidates = append(output.Candidates, explainCandidate{
			ModelID:  candidate.ModelID,
			TargetID: catalog.OpaqueTargetID(candidate.TargetID),
			Provider: candidateProvider(input, candidate.TargetID),
			Accepted: candidate.Accepted,
			Reason:   candidate.Reason,
		})
	}
	for _, target := range plan.Targets {
		output.Targets = append(output.Targets, explainTarget{ModelID: target.ModelID, TargetID: catalog.OpaqueTargetID(target.Target.ID), Provider: target.Target.Provider})
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(output)
}

// candidateProvider 从输入快照读取候选 Provider，避免向解释结果暴露上游模型名。
func candidateProvider(input decision.Input, targetID string) string {
	for _, candidate := range input.Candidates {
		if candidate.Target.ID == targetID {
			return candidate.Target.Provider
		}
	}
	return "unknown"
}
