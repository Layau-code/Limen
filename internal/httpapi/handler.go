package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/provider"
)

const maxRequestBytes = 4 << 20

type Handler struct {
	apiKey string
	router *gateway.Router
}

// New 创建 Limen 的 HTTP 路由和请求处理器。
func New(apiKey string, router *gateway.Router) http.Handler {
	handler := &Handler{apiKey: apiKey, router: router}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", handler.chatCompletions)
	return mux
}

// chatCompletions 鉴权并处理一次 Chat Completions 请求。
func (h *Handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if !validBearerToken(r.Header.Get("Authorization"), h.apiKey) {
		writeError(w, http.StatusUnauthorized, "invalid API key", "authentication_error", "invalid_api_key")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error", "invalid_body")
		return
	}
	request, err := parseChatRequest(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_chat_request")
		return
	}
	if h.router == nil {
		writeError(w, http.StatusBadGateway, "provider unavailable", "api_error", "provider_unavailable")
		return
	}
	h.forward(w, r, request)
}

// validBearerToken 使用常量时间比较校验 Limen API Key。
func validBearerToken(header, expected string) bool {
	token, found := strings.CutPrefix(header, "Bearer ")
	return found && len(token) == len(expected) && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

// forward 调用路由选中的 Provider，并转发普通内容或 SSE 数据。
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, request provider.ChatRequest) {
	response, err := h.router.Chat(r.Context(), request)
	if err != nil {
		var unsupported *gateway.UnsupportedModelError
		if errors.As(err, &unsupported) {
			writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "unsupported_model")
			return
		}
		var unavailable *gateway.ProviderUnavailableError
		if errors.As(err, &unavailable) {
			writeError(w, http.StatusBadGateway, err.Error(), "api_error", "provider_unavailable")
			return
		}
		status := http.StatusBadGateway
		code := "provider_error"
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
			code = "provider_timeout"
		}
		writeError(w, status, "provider request failed", "api_error", code)
		return
	}
	defer response.Body.Close()
	if response.ContentType != "" {
		w.Header().Set("Content-Type", response.ContentType)
	}
	w.WriteHeader(response.StatusCode)
	if strings.HasPrefix(response.ContentType, "text/event-stream") {
		relayStream(w, response.Body)
		return
	}
	_, _ = io.Copy(w, response.Body)
}

type incomingChatRequest struct {
	Model       string            `json:"model"`
	Messages    []incomingMessage `json:"messages"`
	MaxTokens   int               `json:"max_tokens"`
	Temperature *float64          `json:"temperature"`
	Stream      bool              `json:"stream"`
	Tools       json.RawMessage   `json:"tools"`
	ToolChoice  json.RawMessage   `json:"tool_choice"`
}

type incomingMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls json.RawMessage `json:"tool_calls"`
}

// parseChatRequest 将 OpenAI 请求解析为内部统一请求，并拒绝暂不支持的内容。
func parseChatRequest(body []byte) (provider.ChatRequest, error) {
	var incoming incomingChatRequest
	if err := json.Unmarshal(body, &incoming); err != nil {
		return provider.ChatRequest{}, errors.New("invalid JSON request")
	}
	if strings.TrimSpace(incoming.Model) == "" {
		return provider.ChatRequest{}, errors.New("model is required")
	}
	if len(incoming.Messages) == 0 {
		return provider.ChatRequest{}, errors.New("messages are required")
	}
	if len(incoming.Tools) > 0 || len(incoming.ToolChoice) > 0 {
		return provider.ChatRequest{}, errors.New("tools are not supported")
	}
	request := provider.ChatRequest{Model: incoming.Model, MaxTokens: incoming.MaxTokens, Temperature: incoming.Temperature, Stream: incoming.Stream}
	for _, message := range incoming.Messages {
		if len(message.ToolCalls) > 0 {
			return provider.ChatRequest{}, errors.New("tool calls are not supported")
		}
		if message.Role != "system" && message.Role != "user" && message.Role != "assistant" {
			return provider.ChatRequest{}, fmt.Errorf("unsupported message role: %s", message.Role)
		}
		var content string
		if err := json.Unmarshal(message.Content, &content); err != nil {
			return provider.ChatRequest{}, errors.New("message content must be text")
		}
		request.Messages = append(request.Messages, provider.Message{Role: message.Role, Content: content})
	}
	return request, nil
}

// relayStream 逐块转发 SSE 数据，并在每块写入后刷新客户端。
func relayStream(w http.ResponseWriter, source io.Reader) {
	flusher, canFlush := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		count, err := source.Read(buffer)
		if count > 0 {
			if _, writeErr := w.Write(buffer[:count]); writeErr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
