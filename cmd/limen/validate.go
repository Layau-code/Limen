package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/provider"
)

type validateOutput struct {
	ConfigVersion string         `json:"config_version"`
	Models        int            `json:"models"`
	Targets       int            `json:"targets"`
	Providers     map[string]int `json:"providers"`
}

// runValidate 在不加载密钥或访问网络的情况下预检模型目录。
func runValidate(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	modelsPath := flags.String("models", "", "模型目录 JSON 文件路径")
	openAIBaseURL := flags.String("openai-base-url", envOrDefault("OPENAI_BASE_URL", "https://api.openai.com/v1"), "OpenAI Provider 基础地址")
	anthropicBaseURL := flags.String("anthropic-base-url", envOrDefault("ANTHROPIC_BASE_URL", "https://api.anthropic.com"), "Anthropic Provider 基础地址")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("validate 不接受位置参数")
	}
	if *modelsPath == "" {
		return errors.New("validate 需要 --models")
	}
	contents, err := os.ReadFile(*modelsPath)
	if err != nil {
		return fmt.Errorf("读取模型目录失败: %w", err)
	}
	models, _, version, err := config.ParseModels(contents, config.DefaultRouting())
	if err != nil {
		return fmt.Errorf("解析模型目录失败: %w", err)
	}
	registry, err := registryFromConfig(models)
	if err != nil {
		return fmt.Errorf("创建模型目录失败: %w", err)
	}
	endpoints, err := validateProviderEndpoints(*openAIBaseURL, *anthropicBaseURL)
	if err != nil {
		return err
	}
	router := gateway.NewRouter(nil, registry, gateway.Policy{})
	if err := router.SetProviderEndpointIDs(endpoints); err != nil {
		return errors.New("模型目录 endpoint 绑定失败")
	}
	output := validateOutput{
		ConfigVersion: version,
		Models:        len(models),
		Providers:     make(map[string]int),
	}
	for _, model := range models {
		output.Targets += len(model.Targets)
		for _, target := range model.Targets {
			output.Providers[target.Provider]++
		}
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(output)
}

// envOrDefault 返回非空环境变量，否则使用离线预检默认地址。
func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// validateProviderEndpoints 复用启动阶段的 HTTPS 和 endpoint ID 校验。
func validateProviderEndpoints(openAIBaseURL, anthropicBaseURL string) (map[string]string, error) {
	values := []struct {
		name string
		raw  string
	}{
		{name: "openai", raw: openAIBaseURL},
		{name: "anthropic", raw: anthropicBaseURL},
	}
	endpoints := make(map[string]string, len(values))
	for _, value := range values {
		if err := config.ValidateProviderBaseURL(value.raw); err != nil {
			return nil, fmt.Errorf("%s endpoint 无效: %w", value.name, err)
		}
		endpointID, err := provider.EndpointIDForBaseURL(value.raw)
		if err != nil {
			return nil, fmt.Errorf("%s endpoint 无效", value.name)
		}
		endpoints[value.name] = endpointID
	}
	return endpoints, nil
}
