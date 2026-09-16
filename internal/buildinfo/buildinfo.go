// Package buildinfo 提供 Limen 构建版本信息。
package buildinfo

import (
	"encoding/json"
	"io"
)

// 这些变量可通过 go build -ldflags 覆盖，开发构建保持稳定默认值。
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Info 描述可公开展示的构建信息。
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"build_date"`
}

// Current 返回当前进程的构建信息副本。
func Current() Info {
	return Info{Version: Version, Commit: Commit, Date: Date}
}

// WriteJSON 将构建信息按稳定字段格式写入输出。
func WriteJSON(writer io.Writer) error {
	return json.NewEncoder(writer).Encode(Current())
}
