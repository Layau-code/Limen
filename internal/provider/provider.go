package provider

import (
	"context"
	"fmt"
	"io"
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

// Usage 表示 Provider 已经确认的输入和输出 Token。
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	Complete     bool
}

// UsageRecorder 提供线程安全的请求用量快照。
type UsageRecorder interface {
	Snapshot() Usage
}

// Response 表示 Provider 返回的、与 HTTP 框架无关的响应。
type Response struct {
	StatusCode  int
	ContentType string
	Body        io.ReadCloser
	Usage       UsageRecorder
	ErrorClass  ErrorClass
}

// ErrorClass 表示 Provider 对上游失败语义的归一化分类。
type ErrorClass string

const (
	// ErrorClassRetryableTransient 表示允许执行计划中的下一个目标。
	ErrorClassRetryableTransient ErrorClass = "retryable_transient"
	// ErrorClassDeterministicRequest 表示请求本身无须重试。
	ErrorClassDeterministicRequest ErrorClass = "deterministic_request"
	// ErrorClassAuthentication 表示 Provider 凭据或权限错误。
	ErrorClassAuthentication ErrorClass = "authentication"
	// ErrorClassQuota 表示配额或账户限制错误。
	ErrorClassQuota ErrorClass = "quota"
	// ErrorClassInternal 表示未归类的 Provider 内部错误。
	ErrorClassInternal ErrorClass = "internal"
)

// ClassifyHTTPStatus 将上游 HTTP 状态转换为稳定的 Provider 错误分类。
func ClassifyHTTPStatus(status int) ErrorClass {
	switch {
	case status == 401 || status == 403:
		return ErrorClassAuthentication
	case status == 402:
		return ErrorClassQuota
	case status == 408 || status == 409 || status == 429 || status == 500 || status == 502 || status == 503 || status == 504 || status == 529:
		return ErrorClassRetryableTransient
	case status >= 400 && status < 500:
		return ErrorClassDeterministicRequest
	case status >= 500:
		return ErrorClassInternal
	default:
		return ""
	}
}

// IsRetryableResponse 判断响应是否允许切换到执行计划中的下一个目标。
func IsRetryableResponse(response Response) bool {
	if response.ErrorClass != "" {
		return response.ErrorClass == ErrorClassRetryableTransient
	}
	return ClassifyHTTPStatus(response.StatusCode) == ErrorClassRetryableTransient
}

// Provider 定义统一的聊天调用入口。
type Provider interface {
	Chat(context.Context, ChatRequest) (Response, error)
}

// RequestError 表示调用上游前发生的确定性本地请求错误。
type RequestError struct {
	Operation string
	Err       error
}

// Error 返回包含失败操作的请求错误描述。
func (e *RequestError) Error() string {
	return fmt.Sprintf("%s: %v", e.Operation, e.Err)
}

// Unwrap 返回底层错误，便于调用方使用 errors.Is 和 errors.As。
func (e *RequestError) Unwrap() error {
	return e.Err
}

// TransportError 表示请求上游时发生的网络或 Context 错误。
type TransportError struct {
	Operation string
	Err       error
}

// Error 返回包含失败操作的传输错误描述。
func (e *TransportError) Error() string {
	return fmt.Sprintf("%s: %v", e.Operation, e.Err)
}

// Unwrap 返回底层错误，便于调用方识别取消或超时。
func (e *TransportError) Unwrap() error {
	return e.Err
}
