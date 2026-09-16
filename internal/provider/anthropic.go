package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const anthropicVersion = "2023-06-01"

// AnthropicProvider 将统一聊天请求转换为 Anthropic Messages API 请求。
type AnthropicProvider struct {
	client *http.Client
	url    string
	mu     sync.RWMutex
	apiKey string
}

// SetAPIKey 原子替换 Anthropic 凭据，供受控轮换流程使用。
func (p *AnthropicProvider) SetAPIKey(apiKey string) error {
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("Anthropic API key must not be empty")
	}
	p.mu.Lock()
	p.apiKey = apiKey
	p.mu.Unlock()
	return nil
}

// NewAnthropic 创建 Anthropic Provider。
func NewAnthropic(client *http.Client, baseURL, apiKey string) *AnthropicProvider {
	if client == nil {
		client = http.DefaultClient
	}
	return &AnthropicProvider{
		client: client,
		url:    strings.TrimRight(baseURL, "/") + "/v1/messages",
		apiKey: apiKey,
	}
}

// Chat 调用 Anthropic Messages API，并将响应转换为 OpenAI 兼容格式。
func (p *AnthropicProvider) Chat(parent context.Context, request ChatRequest) (Response, error) {
	body, err := marshalAnthropicRequest(request)
	if err != nil {
		return Response{}, &RequestError{Operation: "encode Anthropic request", Err: err}
	}
	httpRequest, err := http.NewRequestWithContext(parent, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return Response{}, &RequestError{Operation: "build Anthropic request", Err: err}
	}
	p.mu.RLock()
	apiKey := p.apiKey
	p.mu.RUnlock()
	httpRequest.Header.Set("x-api-key", apiKey)
	httpRequest.Header.Set("anthropic-version", anthropicVersion)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return Response{}, &TransportError{Operation: "send Anthropic request", Err: err}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		recorder := newUsageRecorder()
		return Response{StatusCode: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: observeJSON(response.Body, recorder), Usage: recorder}, nil
	}
	if request.Stream {
		recorder := newUsageRecorder()
		return Response{StatusCode: response.StatusCode, ContentType: "text/event-stream", Body: translateAnthropicStream(response.Body, recorder), Usage: recorder}, nil
	}
	translated, err := translateAnthropicResponse(response.StatusCode, response.Body)
	if err != nil {
		return Response{}, &RequestError{Operation: "decode Anthropic response", Err: err}
	}
	return translated, nil
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// marshalAnthropicRequest 提取 system 消息并构造 Messages API 请求体。
func marshalAnthropicRequest(request ChatRequest) ([]byte, error) {
	converted := anthropicRequest{Model: request.Model, MaxTokens: request.MaxTokens, Temperature: request.Temperature, Stream: request.Stream}
	if converted.MaxTokens == 0 {
		converted.MaxTokens = 4096
	}
	for _, message := range request.Messages {
		if message.Role == "system" {
			if converted.System != "" {
				converted.System += "\n"
			}
			converted.System += message.Content
			continue
		}
		converted.Messages = append(converted.Messages, anthropicMessage{Role: message.Role, Content: message.Content})
	}
	if len(converted.Messages) == 0 {
		return nil, fmt.Errorf("Anthropic request requires a user or assistant message")
	}
	body, err := json.Marshal(converted)
	if err != nil {
		return nil, fmt.Errorf("encode Anthropic request: %w", err)
	}
	return body, nil
}

type anthropicResponse struct {
	ID         string `json:"id"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// translateAnthropicResponse 将 Anthropic JSON 响应转换为 OpenAI 响应。
func translateAnthropicResponse(status int, source io.ReadCloser) (Response, error) {
	defer source.Close()
	var response anthropicResponse
	if err := json.NewDecoder(source).Decode(&response); err != nil {
		return Response{}, fmt.Errorf("decode Anthropic response: %w", err)
	}
	var text strings.Builder
	for _, block := range response.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	finishReason := anthropicFinishReason(response.StopReason)
	body, err := json.Marshal(map[string]any{
		"id": response.ID, "object": "chat.completion", "created": time.Now().Unix(), "model": response.Model,
		"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": text.String()}, "finish_reason": finishReason}},
		"usage":   map[string]int{"prompt_tokens": response.Usage.InputTokens, "completion_tokens": response.Usage.OutputTokens, "total_tokens": response.Usage.InputTokens + response.Usage.OutputTokens},
	})
	if err != nil {
		return Response{}, fmt.Errorf("encode OpenAI response: %w", err)
	}
	recorder := newUsageRecorder()
	recorder.set(int64(response.Usage.InputTokens), int64(response.Usage.OutputTokens))
	return Response{StatusCode: status, ContentType: "application/json", Body: io.NopCloser(bytes.NewReader(body)), Usage: recorder}, nil
}

type anthropicEvent struct {
	Type    string `json:"type"`
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens int64 `json:"input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Delta struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage struct {
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

// translateAnthropicStream 将 Anthropic SSE 事件转换为 OpenAI SSE 事件。
func translateAnthropicStream(source io.ReadCloser, recorder *usageRecorder) io.ReadCloser {
	reader, writer := io.Pipe()
	go func() {
		defer source.Close()
		defer writer.Close()
		scanner := bufio.NewScanner(source)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		var eventName, data string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				eventName = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "":
				if eventName != "" {
					if err := writeAnthropicEvent(writer, recorder, eventName, data); err != nil {
						_ = writer.CloseWithError(err)
						return
					}
				}
				eventName, data = "", ""
			}
		}
		if err := scanner.Err(); err != nil {
			_ = writer.CloseWithError(err)
		}
	}()
	return &anthropicStreamBody{reader: reader, source: source}
}

type anthropicStreamBody struct {
	reader *io.PipeReader
	source io.ReadCloser
	once   sync.Once
}

// Read 从转换后的 SSE 流中读取 OpenAI 兼容数据。
func (body *anthropicStreamBody) Read(buffer []byte) (int, error) {
	return body.reader.Read(buffer)
}

// Close 同时关闭转换管道和 Anthropic 上游连接。
func (body *anthropicStreamBody) Close() error {
	var err error
	body.once.Do(func() {
		err = body.reader.CloseWithError(io.ErrClosedPipe)
		_ = body.source.Close()
	})
	return err
}

// writeAnthropicEvent 将一个 Anthropic 事件写成 OpenAI SSE 数据块。
func writeAnthropicEvent(writer io.Writer, recorder *usageRecorder, eventName, data string) error {
	var event anthropicEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return fmt.Errorf("decode Anthropic event: %w", err)
	}
	var chunk any
	switch eventName {
	case "message_start":
		recorder.setInput(event.Message.Usage.InputTokens)
		chunk = map[string]any{"id": event.Message.ID, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": event.Message.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"role": "assistant"}, "finish_reason": nil}}}
	case "content_block_delta":
		if event.Delta.Type != "text_delta" {
			return nil
		}
		chunk = map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": event.Delta.Text}, "finish_reason": nil}}}
	case "message_delta":
		recorder.setOutput(event.Usage.OutputTokens)
		chunk = map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{}, "finish_reason": anthropicFinishReason(event.Delta.StopReason)}}}
	case "message_stop":
		_, err := io.WriteString(writer, "data: [DONE]\n\n")
		return err
	default:
		return nil
	}
	body, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "data: %s\n\n", body)
	return err
}

// anthropicFinishReason 将 Anthropic 停止原因映射为 OpenAI 停止原因。
func anthropicFinishReason(reason string) string {
	switch reason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "end_turn", "stop_sequence":
		return "stop"
	default:
		return ""
	}
}
