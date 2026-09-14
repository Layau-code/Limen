package gateway

import (
	"context"
	"fmt"

	"github.com/huz/limen/internal/provider"
)

// Router 根据模型注册表选择对应的 Provider。
type Router struct {
	openAI    provider.Provider
	anthropic provider.Provider
	registry  *ModelRegistry
}

// NewRouter 创建使用指定模型注册表的 Provider 路由器。
func NewRouter(openAI, anthropic provider.Provider, registry *ModelRegistry) *Router {
	return &Router{openAI: openAI, anthropic: anthropic, registry: registry}
}

// Chat 解析逻辑模型，并将请求转发给注册表指定的 Provider。
func (r *Router) Chat(ctx context.Context, request provider.ChatRequest) (provider.Response, error) {
	model, found := r.registry.Resolve(request.Model)
	if !found {
		return provider.Response{}, &UnsupportedModelError{Model: request.Model}
	}
	request.Model = model.UpstreamModel
	switch model.Provider {
	case "openai":
		if r.openAI == nil {
			return provider.Response{}, &ProviderUnavailableError{Name: "OpenAI"}
		}
		return r.openAI.Chat(ctx, request)
	case "anthropic":
		if r.anthropic == nil {
			return provider.Response{}, &ProviderUnavailableError{Name: "Anthropic"}
		}
		return r.anthropic.Chat(ctx, request)
	default:
		return provider.Response{}, &ProviderUnavailableError{Name: model.Provider}
	}
}

// Models 返回 Router 当前公开的模型列表。
func (r *Router) Models() []Model {
	return r.registry.List()
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
