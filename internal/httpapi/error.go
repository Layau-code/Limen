package httpapi

import (
	"encoding/json"
	"net/http"
)

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Message          string `json:"message"`
	Type             string `json:"type"`
	Code             string `json:"code,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
	DecisionID       string `json:"decision_id,omitempty"`
	SettlementStatus string `json:"settlement_status,omitempty"`
}

// writeError 返回统一的 OpenAI 兼容错误响应。
func writeError(w http.ResponseWriter, status int, message, kind, code string) {
	writeErrorWithMetadata(w, status, message, kind, code, errorMetadata{})
}

type errorMetadata struct {
	RequestID        string
	DecisionID       string
	SettlementStatus string
}

// writeErrorWithMetadata 返回带安全请求状态摘要的错误响应。
func writeErrorWithMetadata(w http.ResponseWriter, status int, message, kind, code string, metadata errorMetadata) {
	if recorder, ok := w.(interface{ SetErrorCode(string) }); ok {
		recorder.SetErrorCode(code)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: apiError{
		Message:          message,
		Type:             kind,
		Code:             code,
		RequestID:        metadata.RequestID,
		DecisionID:       metadata.DecisionID,
		SettlementStatus: metadata.SettlementStatus,
	}})
}
