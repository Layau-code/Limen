package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/huz/limen/internal/config"
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
	if _, err := registryFromConfig(models); err != nil {
		return fmt.Errorf("创建模型目录失败: %w", err)
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
