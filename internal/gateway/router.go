package gateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/huz/limen/internal/provider"
)

// Router 根据模型名前缀选择对应的 Provider。
type Router struct {
	openAI    provider.Provider
	anthropic provider.Provider
}

// NewRouter 创建 OpenAI 和 Anthropic Provider 路由器。
func NewRouter(openAI, anthropic provider.Provider) *Router {
	return &Router{openAI: openAI, anthropic: anthropic}
}

// Chat 将聊天请求转发给模型名前缀对应的 Provider。
func (r *Router) Chat(ctx context.Context, request provider.ChatRequest) (provider.Response, error) {
	switch {
	case strings.HasPrefix(request.Model, "gpt-"), strings.HasPrefix(request.Model, "o1-"), strings.HasPrefix(request.Model, "o3-"):
		if r.openAI == nil {
			return provider.Response{}, &ProviderUnavailableError{Name: "OpenAI"}
		}
		return r.openAI.Chat(ctx, request)
	case strings.HasPrefix(request.Model, "claude-"):
		if r.anthropic == nil {
			return provider.Response{}, &ProviderUnavailableError{Name: "Anthropic"}
		}
		return r.anthropic.Chat(ctx, request)
	default:
		return provider.Response{}, &UnsupportedModelError{Model: request.Model}
	}
}

// ProviderUnavailableError 表示目标 Provider 尚未配置。
type ProviderUnavailableError struct {
	Name string
}

// Error 返回面向日志和 API 层的 Provider 错误描述。
func (e *ProviderUnavailableError) Error() string {
	return fmt.Sprintf("provider unavailable: %s", e.Name)
}

// UnsupportedModelError 表示请求的模型没有可用 Provider。
type UnsupportedModelError struct {
	Model string
}

// Error 返回面向日志和 API 层的模型错误描述。
func (e *UnsupportedModelError) Error() string {
	return fmt.Sprintf("unsupported model: %s", e.Model)
}
