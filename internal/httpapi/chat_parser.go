package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/huz/limen/internal/decision"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/provider"
)

// incomingChatRequest 是 OpenAI Chat 请求的严格入站结构。
type incomingChatRequest struct {
	Model               string                 `json:"model"`
	Messages            []incomingMessage      `json:"messages"`
	MaxTokens           *int                   `json:"max_tokens"`
	MaxCompletionTokens *int                   `json:"max_completion_tokens"`
	Temperature         *float64               `json:"temperature"`
	Stream              bool                   `json:"stream"`
	StreamOptions       *incomingStreamOptions `json:"stream_options"`
	Tools               json.RawMessage        `json:"tools"`
	ToolChoice          json.RawMessage        `json:"tool_choice"`
	ResponseFormat      json.RawMessage        `json:"response_format"`
	N                   *int                   `json:"n"`
	Logprobs            *bool                  `json:"logprobs"`
	Limen               *incomingLimen         `json:"limen"`
}

// incomingStreamOptions 是流式用量选项的严格入站结构。
type incomingStreamOptions struct {
	IncludeUsage *bool `json:"include_usage"`
}

// incomingLimen 是 Limen 能力契约的严格入站结构。
type incomingLimen struct {
	RequiredCapabilities  []string `json:"required_capabilities"`
	MinimumQualityTier    int      `json:"minimum_quality_tier"`
	RequiredContextTokens int64    `json:"required_context_tokens"`
	DataClass             string   `json:"data_class"`
	EstimatedInputTokens  int64    `json:"estimated_input_tokens"`
	EstimatedOutputTokens int64    `json:"estimated_output_tokens"`
	Strategy              string   `json:"strategy"`
}

// incomingMessage 是仅允许文本内容的消息结构。
type incomingMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls json.RawMessage `json:"tool_calls"`
}

// parsedChatRequest 保存解析后的请求、能力契约和运行时快照。
type parsedChatRequest struct {
	Request       provider.ChatRequest
	Contract      decision.Contract
	Run           decision.RunSnapshot
	Registry      *gateway.ModelRegistry
	ConfigVersion string
	Policy        *gateway.Policy
}

// unsupportedFieldError 表示当前兼容子集明确拒绝的请求字段。
type unsupportedFieldError struct {
	Field string
}

// Error 返回稳定且不回显用户输入的字段错误。
func (err *unsupportedFieldError) Error() string {
	switch err.Field {
	case "tools/tool_choice", "response_format", "n", "logprobs", "messages.tool_calls":
		return err.Field + " is not supported"
	default:
		return "unsupported request field"
	}
}

// parseChatRequest 将 OpenAI 请求解析为内部统一请求，并拒绝暂不支持的内容。
func parseChatRequest(body []byte) (provider.ChatRequest, error) {
	envelope, err := parseChatRequestEnvelope(body)
	if err != nil {
		return provider.ChatRequest{}, err
	}
	return envelope.Request, nil
}

// ParseChatRequest 复用 Chat API 的严格解析规则，供离线工具读取请求快照。
func ParseChatRequest(body []byte) (provider.ChatRequest, decision.Contract, error) {
	envelope, err := parseChatRequestEnvelope(body)
	if err != nil {
		return provider.ChatRequest{}, decision.Contract{}, err
	}
	return envelope.Request, envelope.Contract, nil
}

// parseChatRequestEnvelope 严格解析请求并提取 Limen 能力契约。
func parseChatRequestEnvelope(body []byte) (parsedChatRequest, error) {
	var incoming incomingChatRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&incoming); err != nil {
		if field := unknownJSONField(err); field != "" {
			return parsedChatRequest{}, &unsupportedFieldError{Field: field}
		}
		return parsedChatRequest{}, errors.New("invalid JSON request")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return parsedChatRequest{}, errors.New("invalid JSON request")
	}
	if strings.TrimSpace(incoming.Model) == "" {
		return parsedChatRequest{}, errors.New("model is required")
	}
	if len(incoming.Messages) == 0 {
		return parsedChatRequest{}, errors.New("messages are required")
	}
	if len(incoming.Tools) > 0 || len(incoming.ToolChoice) > 0 {
		return parsedChatRequest{}, &unsupportedFieldError{Field: "tools/tool_choice"}
	}
	if len(incoming.ResponseFormat) > 0 {
		return parsedChatRequest{}, &unsupportedFieldError{Field: "response_format"}
	}
	if incoming.N != nil {
		return parsedChatRequest{}, &unsupportedFieldError{Field: "n"}
	}
	if incoming.Logprobs != nil {
		return parsedChatRequest{}, &unsupportedFieldError{Field: "logprobs"}
	}
	if incoming.MaxTokens != nil && incoming.MaxCompletionTokens != nil {
		return parsedChatRequest{}, errors.New("max_tokens and max_completion_tokens are mutually exclusive")
	}
	if incoming.StreamOptions != nil {
		if !incoming.Stream {
			return parsedChatRequest{}, errors.New("stream_options requires stream=true")
		}
		if incoming.StreamOptions.IncludeUsage == nil {
			return parsedChatRequest{}, errors.New("stream_options.include_usage is required")
		}
	}
	request := provider.ChatRequest{Model: incoming.Model, Temperature: incoming.Temperature, Stream: incoming.Stream}
	if incoming.MaxTokens != nil {
		request.MaxTokens = *incoming.MaxTokens
	}
	if incoming.MaxCompletionTokens != nil {
		request.MaxCompletionTokens = incoming.MaxCompletionTokens
	}
	if incoming.StreamOptions != nil {
		request.StreamIncludeUsage = incoming.StreamOptions.IncludeUsage
	}
	for _, message := range incoming.Messages {
		if len(message.ToolCalls) > 0 {
			return parsedChatRequest{}, &unsupportedFieldError{Field: "messages.tool_calls"}
		}
		if message.Role != "system" && message.Role != "developer" && message.Role != "user" && message.Role != "assistant" {
			return parsedChatRequest{}, errors.New("unsupported message role")
		}
		var content string
		if err := json.Unmarshal(message.Content, &content); err != nil {
			return parsedChatRequest{}, errors.New("message content must be text")
		}
		request.Messages = append(request.Messages, provider.Message{Role: message.Role, Content: content})
	}
	contract := decision.Contract{Active: incoming.Model == "auto" || incoming.Limen != nil}
	if incoming.Limen != nil {
		contract.RequiredCapabilities = append([]string(nil), incoming.Limen.RequiredCapabilities...)
		contract.MinimumQualityTier = incoming.Limen.MinimumQualityTier
		contract.RequiredContextTokens = incoming.Limen.RequiredContextTokens
		contract.DataClass = incoming.Limen.DataClass
		contract.EstimatedInputTokens = incoming.Limen.EstimatedInputTokens
		contract.EstimatedOutputTokens = incoming.Limen.EstimatedOutputTokens
		contract.Strategy = incoming.Limen.Strategy
	}
	return parsedChatRequest{Request: request, Contract: contract}, nil
}

// unknownJSONField 将严格 JSON 解码报告的未知字段提取为稳定的 API 字段名。
func unknownJSONField(err error) string {
	const prefix = "json: unknown field "
	message := err.Error()
	if !strings.HasPrefix(message, prefix) {
		return ""
	}
	field, unquoteErr := strconv.Unquote(strings.TrimPrefix(message, prefix))
	if unquoteErr != nil {
		return ""
	}
	return field
}

// relayStream 逐块转发 SSE 数据，并在每块写入后刷新客户端。
func relayStream(w http.ResponseWriter, source io.Reader) error {
	flusher, canFlush := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		count, err := source.Read(buffer)
		if count > 0 {
			if _, writeErr := w.Write(buffer[:count]); writeErr != nil {
				return writeErr
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
