package provider

import (
	"context"
	"io"
	"time"
)

// Message 表示 Limen 内部统一使用的文本消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest 表示经过 API 层校验后的聊天请求。
type ChatRequest struct {
	Model       string
	Messages    []Message
	MaxTokens   int
	Temperature *float64
	Stream      bool
}

// Response 表示 Provider 返回的、与 HTTP 框架无关的响应。
type Response struct {
	StatusCode  int
	ContentType string
	Body        io.ReadCloser
}

// Provider 定义统一的聊天调用入口。
type Provider interface {
	Chat(context.Context, ChatRequest) (Response, error)
}

// requestContext 为上游请求创建可取消的超时上下文。
func requestContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}
