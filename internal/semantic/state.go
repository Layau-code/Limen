// Package semantic contains bounded, privacy-minimized semantic assessment primitives.
package semantic

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/huz/limen/internal/provider"
)

const (
	maxStateBytes   = 4096
	maxUserMessages = 3
)

// State is a short-lived Jev request body and a safe metadata summary. Text must never be persisted or logged.
type State struct {
	Text      string
	Hash      string
	Length    int
	Truncated bool
	Language  string
}

// BuildState 选择最近的用户和助手上下文，并将外发文本限制在 4 KiB。
func BuildState(messages []provider.Message) (State, bool) {
	lastUser := -1
	userCount := 0
	start := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			if lastUser < 0 {
				lastUser = i
			}
			userCount++
			if userCount == maxUserMessages {
				start = i
				break
			}
		}
	}
	if lastUser < 0 {
		return State{}, false
	}
	if strings.TrimSpace(messages[lastUser].Content) == "" || len(messages[lastUser].Content) > maxStateBytes {
		return State{}, false
	}
	selected := make([]provider.Message, 0, len(messages)-start)
	for _, message := range messages[start : lastUser+1] {
		if (message.Role == "user" || message.Role == "assistant") && strings.TrimSpace(message.Content) != "" {
			selected = append(selected, message)
		}
	}
	truncated := false
	for _, message := range messages[:start] {
		if (message.Role == "user" || message.Role == "assistant") && strings.TrimSpace(message.Content) != "" {
			truncated = true
			break
		}
	}
	for len(selected) > 0 {
		text := formatMessages(selected)
		if len(text) <= maxStateBytes {
			return makeState(text, truncated), true
		}
		if len(selected) > 1 {
			selected = selected[1:]
			truncated = true
			continue
		}
		// The latest user text itself fit the cap; reserve room for the role prefix.
		message := selected[0]
		prefix := message.Role + ": "
		remaining := maxStateBytes - len(prefix)
		message.Content = truncateUTF8(message.Content, remaining)
		text = prefix + message.Content
		return makeState(text, true), true
	}
	return State{}, false
}

// formatMessages 使用稳定角色前缀序列化选中的对话消息。
func formatMessages(messages []provider.Message) string {
	var builder strings.Builder
	for i, message := range messages {
		if i > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(message.Role)
		builder.WriteString(": ")
		builder.WriteString(message.Content)
	}
	return builder.String()
}

// truncateUTF8 在字节上限内截断文本并保持 UTF-8 有效。
func truncateUTF8(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// makeState 生成带哈希、长度和语言标签的短期状态。
func makeState(text string, truncated bool) State {
	sum := sha256.Sum256([]byte(text))
	languageText := strings.ReplaceAll(text, "user: ", "")
	languageText = strings.ReplaceAll(languageText, "assistant: ", "")
	return State{Text: text, Hash: "sha256:" + hex.EncodeToString(sum[:]), Length: len(text), Truncated: truncated, Language: detectLanguage(languageText)}
}

// detectLanguage 根据汉字和拉丁字母返回粗粒度语言标签。
func detectLanguage(text string) string {
	var han, latin int
	for _, char := range text {
		if unicode.Is(unicode.Han, char) {
			han++
		}
		if unicode.Is(unicode.Latin, char) {
			latin++
		}
	}
	switch {
	case han == 0 && latin == 0:
		return "unknown"
	case han > 0 && latin > 0:
		return "mixed"
	case han > 0:
		return "zh"
	default:
		return "en"
	}
}
