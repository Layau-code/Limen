package httpapi

import (
	"encoding/json"
	"net/http"
)

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// writeError 返回统一的 OpenAI 兼容错误响应。
func writeError(w http.ResponseWriter, status int, message, kind, code string) {
	if recorder, ok := w.(interface{ SetErrorCode(string) }); ok {
		recorder.SetErrorCode(code)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: apiError{
		Message: message,
		Type:    kind,
		Code:    code,
	}})
}
