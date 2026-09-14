package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIProvider 将统一聊天请求转发到 OpenAI Chat Completions API。
type OpenAIProvider struct {
	client  *http.Client
	url     string
	apiKey  string
	timeout time.Duration
}

// NewOpenAI 创建 OpenAI Provider。
func NewOpenAI(client *http.Client, baseURL, apiKey string, timeout time.Duration) *OpenAIProvider {
	return &OpenAIProvider{
		client:  client,
		url:     strings.TrimRight(baseURL, "/") + "/chat/completions",
		apiKey:  apiKey,
		timeout: timeout,
	}
}

// Chat 将标准化请求编码为 OpenAI 请求，并返回上游响应正文。
func (p *OpenAIProvider) Chat(parent context.Context, request ChatRequest) (Response, error) {
	body, err := json.Marshal(openAIRequest{
		Model:       request.Model,
		Messages:    request.Messages,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		Stream:      request.Stream,
	})
	if err != nil {
		return Response{}, fmt.Errorf("encode OpenAI request: %w", err)
	}
	ctx, cancel := requestContext(parent, p.timeout)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		cancel()
		return Response{}, fmt.Errorf("build OpenAI request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(httpRequest)
	if err != nil {
		cancel()
		return Response{}, fmt.Errorf("send OpenAI request: %w", err)
	}
	response.Body = &cancelOnClose{body: response.Body, cancel: cancel}
	return Response{StatusCode: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: response.Body}, nil
}

type openAIRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type cancelOnClose struct {
	body   io.ReadCloser
	cancel context.CancelFunc
}

// Read 从上游响应中读取数据。
func (body *cancelOnClose) Read(buffer []byte) (int, error) {
	return body.body.Read(buffer)
}

// Close 关闭上游响应并释放关联上下文。
func (body *cancelOnClose) Close() error {
	err := body.body.Close()
	body.cancel()
	return err
}
