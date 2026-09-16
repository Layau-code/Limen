package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/huz/limen/internal/cost"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/provider"
)

const maxRequestBytes = 4 << 20

var settlementTrailerNames = []string{
	"X-Limen-Settlement-Status",
	"X-Limen-Input-Tokens",
	"X-Limen-Output-Tokens",
	"X-Limen-Total-Tokens",
	"X-Limen-Cost-USD",
}

type Handler struct {
	apiKey string
	router *gateway.Router
}

// New 创建 Limen 的 HTTP 路由和请求处理器。
func New(apiKey string, router *gateway.Router) http.Handler {
	return NewWithHealth(apiKey, router, nil)
}

// NewWithHealth 创建带健康检查端点的 Limen HTTP 处理器。
func NewWithHealth(apiKey string, router *gateway.Router, health *Health) http.Handler {
	if health == nil {
		health = NewHealth()
		health.SetReady(true)
	}
	handler := &Handler{apiKey: apiKey, router: router}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", handler.chatCompletions)
	mux.HandleFunc("GET /v1/models", handler.models)
	mux.HandleFunc("GET /livez", health.Live)
	mux.HandleFunc("GET /readyz", health.Ready)
	return mux
}

// chatCompletions 鉴权并处理一次 Chat Completions 请求。
func (h *Handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if !h.authenticate(w, r) {
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

// models 鉴权并返回当前可用的 OpenAI 兼容模型列表。
func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	if !h.authenticate(w, r) {
		return
	}
	if h.router == nil {
		writeError(w, http.StatusBadGateway, "provider unavailable", "api_error", "provider_unavailable")
		return
	}
	response := modelsResponse{Object: "list"}
	for _, model := range h.router.Models() {
		ownedBy := "limen"
		if model.Compatibility && len(model.Targets) > 0 {
			ownedBy = model.Targets[0].Provider
		}
		response.Data = append(response.Data, modelResponse{
			ID:      model.ID,
			Object:  "model",
			Created: 0,
			OwnedBy: ownedBy,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// authenticate 校验请求中的 Limen Bearer Key，并在失败时写入统一错误。
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) bool {
	if validBearerToken(r.Header.Get("Authorization"), h.apiKey) {
		return true
	}
	writeError(w, http.StatusUnauthorized, "invalid API key", "authentication_error", "invalid_api_key")
	return false
}

// validBearerToken 使用常量时间比较校验 Limen API Key。
func validBearerToken(header, expected string) bool {
	if expected == "" {
		return false
	}
	token, found := strings.CutPrefix(header, "Bearer ")
	return found && len(token) == len(expected) && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

// forward 调用路由选中的 Provider，并转发普通内容或 SSE 数据。
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, request provider.ChatRequest) {
	result, err := h.router.Chat(r.Context(), request)
	if err != nil {
		var unsupported *gateway.UnsupportedModelError
		if errors.As(err, &unsupported) {
			writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "unsupported_model")
			return
		}
		var unavailable *gateway.ProviderUnavailableError
		if errors.As(err, &unavailable) {
			writeRouteHeaders(w, unavailable.Decision)
			writeError(w, http.StatusBadGateway, err.Error(), "api_error", "provider_unavailable")
			return
		}
		var noTarget *gateway.NoAvailableTargetError
		if errors.As(err, &noTarget) {
			writeRouteHeaders(w, noTarget.Decision)
			writeError(w, http.StatusServiceUnavailable, err.Error(), "api_error", "no_available_target")
			return
		}
		status := http.StatusBadGateway
		code := "provider_error"
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
			code = "provider_timeout"
		} else if errors.Is(err, context.Canceled) {
			status = 499
			code = "client_canceled"
		}
		var routeError *gateway.RouteError
		if errors.As(err, &routeError) {
			writeRouteHeaders(w, routeError.Decision)
		}
		writeError(w, status, "provider request failed", "api_error", code)
		return
	}
	response := result.Response
	writeRouteHeaders(w, result.Decision)
	defer response.Body.Close()
	if response.ContentType != "" {
		w.Header().Set("Content-Type", response.ContentType)
	}
	declareSettlementTrailers(w)
	w.WriteHeader(response.StatusCode)
	if strings.HasPrefix(response.ContentType, "text/event-stream") {
		relayStream(w, response.Body)
	} else {
		_, _ = io.Copy(w, response.Body)
	}
	writeSettlementTrailers(w, result.Settlement)
}

// declareSettlementTrailers 在响应开始前声明请求结束后可用的结算字段。
func declareSettlementTrailers(w http.ResponseWriter) {
	w.Header().Set("Trailer", strings.Join(settlementTrailerNames, ", "))
}

// writeSettlementTrailers 在响应体转发完成后发布结算快照。
func writeSettlementTrailers(w http.ResponseWriter, settlement *gateway.Settlement) {
	if settlement == nil {
		w.Header().Set("X-Limen-Settlement-Status", string(gateway.SettlementUnavailable))
		return
	}
	summary := settlement.Summary()
	w.Header().Set("X-Limen-Settlement-Status", string(summary.Status))
	if summary.Status == gateway.SettlementUnavailable {
		return
	}
	w.Header().Set("X-Limen-Input-Tokens", strconv.FormatInt(summary.InputTokens, 10))
	w.Header().Set("X-Limen-Output-Tokens", strconv.FormatInt(summary.OutputTokens, 10))
	w.Header().Set("X-Limen-Total-Tokens", strconv.FormatInt(summary.TotalTokens, 10))
	if summary.CostAvailable {
		w.Header().Set("X-Limen-Cost-USD", cost.FormatUSD(summary.CostNanoUSD))
	}
}

// writeRouteHeaders 暴露不含模型、密钥和正文的路由摘要。
func writeRouteHeaders(w http.ResponseWriter, decision gateway.Decision) {
	if decision.Provider != "" {
		w.Header().Set("X-Limen-Provider", decision.Provider)
	}
	if decision.Attempts > 0 {
		w.Header().Set("X-Limen-Attempts", strconv.Itoa(decision.Attempts))
	}
	if route := decision.String(); route != "" {
		w.Header().Set("X-Limen-Route", route)
	}
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

type modelsResponse struct {
	Object string          `json:"object"`
	Data   []modelResponse `json:"data"`
}

type modelResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
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
