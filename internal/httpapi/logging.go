package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
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
		requestID := safeRequestID(r.Header.Get("X-Request-ID"))
		r.Header.Set("X-Request-ID", requestID)
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
		if firstByte := recorder.FirstByteAt(); !firstByte.IsZero() {
			attrs = append(attrs, "ttfb_ms", firstByte.Sub(started).Milliseconds())
		}
		for _, name := range []string{"X-Limen-Provider", "X-Limen-Attempts", "X-Limen-Route", "X-Limen-Decision-ID", "X-Limen-Config-Version"} {
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

// safeRequestID 将客户端标识压缩为不可逆摘要，避免任意输入进入日志和 Trace。
func safeRequestID(value string) string {
	if value == "" || len(value) > 256 {
		return newRequestID()
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

func logHeaderKey(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(name, "X-Limen-")), "-", "_")
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	errorCode   string
	firstByteAt time.Time
}

// WriteHeader 记录响应状态，并将状态写入底层 ResponseWriter。
func (w *statusRecorder) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

// Write 记录隐式的成功状态后写入响应体。
func (w *statusRecorder) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.firstByteAt.IsZero() {
		w.firstByteAt = time.Now()
	}
	return w.ResponseWriter.Write(data)
}

// SetErrorCode 保存由网关生成的稳定错误码，供低基数指标使用。
func (w *statusRecorder) SetErrorCode(code string) {
	w.errorCode = code
}

// FirstByteAt 返回首次尝试写出响应正文的时间。
func (w *statusRecorder) FirstByteAt() time.Time {
	return w.firstByteAt
}

// Flush 将已写入的数据刷新给客户端，保持流式响应的及时性。
func (w *statusRecorder) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
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
