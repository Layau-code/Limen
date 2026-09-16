package httpapi

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// Health 保存进程是否已经完成启动和依赖初始化的状态。
type Health struct {
	ready atomic.Bool
}

// NewHealth 创建一个初始为未就绪的健康状态。
func NewHealth() *Health {
	return &Health{}
}

// SetReady 更新服务是否可以接收业务流量。
func (health *Health) SetReady(ready bool) {
	health.ready.Store(ready)
}

// Live 返回进程存活状态；该接口不依赖 Provider 或 API Key。
func (health *Health) Live(w http.ResponseWriter, _ *http.Request) {
	writeHealth(w, http.StatusOK, "ok")
}

// Ready 返回依赖是否初始化完成；关闭流程会先将其置为未就绪。
func (health *Health) Ready(w http.ResponseWriter, _ *http.Request) {
	if !health.ready.Load() {
		writeHealth(w, http.StatusServiceUnavailable, "not_ready")
		return
	}
	writeHealth(w, http.StatusOK, "ok")
}

// writeHealth 输出健康检查使用的最小 JSON 响应。
func writeHealth(w http.ResponseWriter, status int, state string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Status string `json:"status"`
	}{Status: state})
}
