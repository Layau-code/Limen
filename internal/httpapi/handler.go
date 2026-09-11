package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/huz/limen/internal/gateway"
)

const maxRequestBytes = 4 << 20

type Handler struct {
	apiKey  string
	service *gateway.Service
}

// New 创建 Limen 的 HTTP 路由和请求处理器。
func New(apiKey string, service *gateway.Service) http.Handler {
	handler := &Handler{apiKey: apiKey, service: service}
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
	var request struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &request); err != nil || strings.TrimSpace(request.Model) == "" {
		writeError(w, http.StatusBadRequest, "model is required", "invalid_request_error", "invalid_model")
		return
	}
	if h.service == nil {
		writeError(w, http.StatusBadGateway, "provider unavailable", "api_error", "provider_unavailable")
		return
	}
	h.forward(w, r, body)
}

// validBearerToken 使用常量时间比较校验 Limen API Key。
func validBearerToken(header, expected string) bool {
	token, found := strings.CutPrefix(header, "Bearer ")
	return found && len(token) == len(expected) && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

// forward 调用上游服务，并按响应类型转发普通内容或 SSE 数据。
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, body []byte) {
	response, err := h.service.Forward(r.Context(), body)
	if err != nil {
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
	for _, value := range response.Header.Values("Content-Type") {
		w.Header().Add("Content-Type", value)
	}
	w.WriteHeader(response.StatusCode)
	if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		relayStream(w, response.Body)
		return
	}
	_, _ = io.Copy(w, response.Body)
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
