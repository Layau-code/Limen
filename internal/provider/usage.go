package provider

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
)

const (
	maxObservedResponseBytes = 1 << 20
	maxObservedSSEBytes      = 1 << 20
)

type usageRecorder struct {
	mu        sync.RWMutex
	usage     Usage
	inputSet  bool
	outputSet bool
}

// newUsageRecorder 创建一次 Provider 调用使用的线程安全用量记录器。
func newUsageRecorder() *usageRecorder {
	return &usageRecorder{}
}

// Snapshot 返回当前已经确认的用量，不复制任何响应内容。
func (recorder *usageRecorder) Snapshot() Usage {
	recorder.mu.RLock()
	defer recorder.mu.RUnlock()
	return recorder.usage
}

// set 写入一组完整且非负的 Provider 用量。
func (recorder *usageRecorder) set(inputTokens, outputTokens int64) {
	if inputTokens < 0 || outputTokens < 0 {
		return
	}
	recorder.mu.Lock()
	recorder.usage.InputTokens = inputTokens
	recorder.usage.OutputTokens = outputTokens
	recorder.inputSet = true
	recorder.outputSet = true
	recorder.usage.Complete = true
	recorder.mu.Unlock()
}

// setInput 写入流式响应已经确认的输入 Token。
func (recorder *usageRecorder) setInput(inputTokens int64) {
	if inputTokens < 0 {
		return
	}
	recorder.mu.Lock()
	recorder.usage.InputTokens = inputTokens
	recorder.inputSet = true
	recorder.usage.Complete = recorder.inputSet && recorder.outputSet
	recorder.mu.Unlock()
}

// setOutput 写入流式响应已经确认的输出 Token。
func (recorder *usageRecorder) setOutput(outputTokens int64) {
	if outputTokens < 0 {
		return
	}
	recorder.mu.Lock()
	recorder.usage.OutputTokens = outputTokens
	recorder.outputSet = true
	recorder.usage.Complete = recorder.inputSet && recorder.outputSet
	recorder.mu.Unlock()
}

type observedJSONBody struct {
	source   io.ReadCloser
	recorder *usageRecorder
	buffer   []byte
	overflow bool
	finished bool
	once     sync.Once
}

// observeJSON 包装普通 JSON 响应，并在固定大小内观察 usage 字段。
func observeJSON(source io.ReadCloser, recorder *usageRecorder) io.ReadCloser {
	return &observedJSONBody{source: source, recorder: recorder}
}

// Read 转发普通响应，同时在 EOF 时解析有界观察内容。
func (body *observedJSONBody) Read(buffer []byte) (int, error) {
	count, err := body.source.Read(buffer)
	if count > 0 && !body.overflow {
		if len(body.buffer)+count > maxObservedResponseBytes {
			body.overflow = true
			body.buffer = nil
		} else {
			body.buffer = append(body.buffer, buffer[:count]...)
		}
	}
	if err == io.EOF && !body.finished {
		body.finished = true
		if !body.overflow {
			setOpenAIUsage(body.recorder, body.buffer)
		}
	}
	return count, err
}

// Close 关闭普通响应的上游连接，并且只执行一次。
func (body *observedJSONBody) Close() error {
	var err error
	body.once.Do(func() { err = body.source.Close() })
	return err
}

type observedSSEBody struct {
	source      io.ReadCloser
	recorder    *usageRecorder
	line        []byte
	lineTooLong bool
	event       []byte
	eventTooBig bool
	finished    bool
	once        sync.Once
}

// observeOpenAISSE 包装 OpenAI SSE，并只解析当前事件中的 usage。
func observeOpenAISSE(source io.ReadCloser, recorder *usageRecorder) io.ReadCloser {
	return &observedSSEBody{source: source, recorder: recorder}
}

// Read 转发 SSE 字节，并在事件边界解析用量。
func (body *observedSSEBody) Read(buffer []byte) (int, error) {
	count, err := body.source.Read(buffer)
	if count > 0 {
		body.feed(buffer[:count])
	}
	if err == io.EOF && !body.finished {
		body.finished = true
		if len(body.line) > 0 && !body.lineTooLong {
			body.processLine(body.line)
		}
		body.finishEvent()
	}
	return count, err
}

// Close 关闭 SSE 上游连接，并且只执行一次。
func (body *observedSSEBody) Close() error {
	var err error
	body.once.Do(func() { err = body.source.Close() })
	return err
}

func (body *observedSSEBody) feed(buffer []byte) {
	for _, character := range buffer {
		if character == '\n' {
			if !body.lineTooLong {
				body.processLine(body.line)
			}
			body.line = body.line[:0]
			body.lineTooLong = false
			continue
		}
		if len(body.line) >= maxObservedSSEBytes {
			body.lineTooLong = true
			continue
		}
		body.line = append(body.line, character)
	}
}

func (body *observedSSEBody) processLine(line []byte) {
	line = bytesTrimSuffixCR(line)
	if len(line) == 0 {
		body.finishEvent()
		return
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	value := line[len("data:"):]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	if len(body.event)+len(value)+1 > maxObservedSSEBytes {
		body.eventTooBig = true
		return
	}
	if len(body.event) > 0 {
		body.event = append(body.event, '\n')
	}
	body.event = append(body.event, value...)
}

func (body *observedSSEBody) finishEvent() {
	if !body.eventTooBig && len(body.event) > 0 && string(body.event) != "[DONE]" {
		setOpenAIUsage(body.recorder, body.event)
	}
	body.event = body.event[:0]
	body.eventTooBig = false
}

func setOpenAIUsage(recorder *usageRecorder, body []byte) {
	var response struct {
		Usage *struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &response) != nil || response.Usage == nil {
		return
	}
	recorder.set(response.Usage.PromptTokens, response.Usage.CompletionTokens)
}

func bytesTrimSuffixCR(value []byte) []byte {
	if len(value) > 0 && value[len(value)-1] == '\r' {
		return value[:len(value)-1]
	}
	return value
}
