package run

import "sync"

// CancellationHub 将跨实例取消事件广播给当前进程的在途请求。
type CancellationHub struct {
	mu          sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
}

// NewCancellationHub 创建空的进程内取消广播器。
func NewCancellationHub() *CancellationHub {
	return &CancellationHub{subscribers: make(map[string]map[chan struct{}]struct{})}
}

// Subscribe 订阅指定租户 Run 的取消信号，并返回退订函数。
func (hub *CancellationHub) Subscribe(tenantID, runID string) (<-chan struct{}, func()) {
	channel := make(chan struct{})
	if hub == nil {
		return channel, func() {}
	}
	key := resourceKey(tenantID, runID)
	hub.mu.Lock()
	if hub.subscribers[key] == nil {
		hub.subscribers[key] = make(map[chan struct{}]struct{})
	}
	hub.subscribers[key][channel] = struct{}{}
	hub.mu.Unlock()
	var once sync.Once
	return channel, func() {
		once.Do(func() {
			hub.mu.Lock()
			defer hub.mu.Unlock()
			if subscribers := hub.subscribers[key]; subscribers != nil {
				delete(subscribers, channel)
				if len(subscribers) == 0 {
					delete(hub.subscribers, key)
				}
			}
		})
	}
}

// Publish 广播取消事件，通知不会携带 Prompt、Key 或其他业务正文。
func (hub *CancellationHub) Publish(event CancellationEvent) {
	if hub == nil {
		return
	}
	key := resourceKey(event.TenantID, event.RunID)
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for channel := range hub.subscribers[key] {
		close(channel)
		delete(hub.subscribers[key], channel)
	}
	delete(hub.subscribers, key)
}
