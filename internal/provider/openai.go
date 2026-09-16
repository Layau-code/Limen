package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// OpenAIProvider 将统一聊天请求转发到 OpenAI Chat Completions API。
type OpenAIProvider struct {
	client *http.Client
	url    string
	apiKey string
}

// NewOpenAI 创建 OpenAI Provider。
func NewOpenAI(client *http.Client, baseURL, apiKey string) *OpenAIProvider {
	if client == nil {
		client = http.DefaultClient
	}
	return &OpenAIProvider{
		client: client,
		url:    strings.TrimRight(baseURL, "/") + "/chat/completions",
		apiKey: apiKey,
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
		return Response{}, &RequestError{Operation: "encode OpenAI request", Err: err}
	}
	httpRequest, err := http.NewRequestWithContext(parent, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return Response{}, &RequestError{Operation: "build OpenAI request", Err: err}
	}
	httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return Response{}, &TransportError{Operation: "send OpenAI request", Err: err}
	}
	return Response{StatusCode: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: response.Body}, nil
}

type openAIRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}
