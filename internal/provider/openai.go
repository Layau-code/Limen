package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
)

// OpenAIProvider 将统一聊天请求转发到 OpenAI Chat Completions API。
type OpenAIProvider struct {
	client *http.Client
	url    string
	mu     sync.RWMutex
	apiKey string
}

// SetAPIKey 原子替换 OpenAI 凭据，供受控轮换流程使用。
func (p *OpenAIProvider) SetAPIKey(apiKey string) error {
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("OpenAI API key must not be empty")
	}
	p.mu.Lock()
	p.apiKey = apiKey
	p.mu.Unlock()
	return nil
}

// ClearAPIKey 清除 OpenAI Provider 内存中的当前密钥。
func (p *OpenAIProvider) ClearAPIKey() {
	p.mu.Lock()
	p.apiKey = ""
	p.mu.Unlock()
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
	upstreamRequest := openAIRequest{
		Model:       request.Model,
		Messages:    request.Messages,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		Stream:      request.Stream,
	}
	if request.Stream {
		upstreamRequest.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	body, err := json.Marshal(upstreamRequest)
	if err != nil {
		return Response{}, &RequestError{Operation: "encode OpenAI request", Err: err}
	}
	httpRequest, err := http.NewRequestWithContext(parent, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return Response{}, &RequestError{Operation: "build OpenAI request", Err: err}
	}
	p.mu.RLock()
	apiKey := p.apiKey
	p.mu.RUnlock()
	httpRequest.Header.Set("Authorization", "Bearer "+apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return Response{}, &TransportError{Operation: "send OpenAI request", Err: err}
	}
	recorder := newUsageRecorder()
	bodyReader := observeJSON(response.Body, recorder)
	if request.Stream {
		bodyReader = observeOpenAISSE(response.Body, recorder)
	}
	return Response{StatusCode: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: bodyReader, Usage: recorder, ErrorClass: ClassifyHTTPStatus(response.StatusCode)}, nil
}

type openAIRequest struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}
