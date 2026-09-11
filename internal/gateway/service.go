package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Service struct {
	client  *http.Client
	url     string
	apiKey  string
	timeout time.Duration
}

// New 创建一个用于访问 OpenAI Chat Completions 接口的服务。
func New(client *http.Client, baseURL, apiKey string, timeout time.Duration) *Service {
	return &Service{
		client:  client,
		url:     strings.TrimRight(baseURL, "/") + "/chat/completions",
		apiKey:  apiKey,
		timeout: timeout,
	}
}

// Forward 将已校验的请求体转发到 OpenAI，并传播调用方的取消信号。
func (s *Service) Forward(ctx context.Context, body []byte) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("build provider request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+s.apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := s.client.Do(request)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("send provider request: %w", err)
	}
	response.Body = &cancelOnClose{body: response.Body, cancel: cancel}
	return response, nil
}

type cancelOnClose struct {
	body   io.ReadCloser
	cancel context.CancelFunc
}

// Read 从上游响应体读取数据。
func (body *cancelOnClose) Read(buffer []byte) (int, error) {
	return body.body.Read(buffer)
}

// Close 关闭上游响应体，并释放关联的请求上下文。
func (body *cancelOnClose) Close() error {
	err := body.body.Close()
	body.cancel()
	return err
}
