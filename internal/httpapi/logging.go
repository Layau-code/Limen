package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// WithLogging 为请求补充 Request ID，并记录不含敏感内容的访问日志。
func WithLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = newRequestID()
		}
		w.Header().Set("X-Request-ID", requestID)
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		attrs := []any{
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(started).Milliseconds(),
		}
		for _, name := range []string{"X-Limen-Provider", "X-Limen-Attempts", "X-Limen-Route", "X-Limen-Decision-ID"} {
			if value := recorder.Header().Get(name); value != "" {
				attrs = append(attrs, logHeaderKey(name), value)
			}
		}
		for _, name := range settlementTrailerNames {
			if value := recorder.Header().Get(name); value != "" {
				attrs = append(attrs, logHeaderKey(name), value)
			}
		}
		logger.Info("request completed", attrs...)
	})
}

func logHeaderKey(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(name, "X-Limen-")), "-", "_")
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader 记录响应状态，并将状态写入底层 ResponseWriter。
func (w *statusRecorder) WriteHeader(status int) {
	if w.status != http.StatusOK {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Flush 将已写入的数据刷新给客户端，保持流式响应的及时性。
func (w *statusRecorder) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// newRequestID 生成一个不包含业务数据的随机请求标识。
func newRequestID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(value[:])
}
