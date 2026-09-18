package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/huz/limen/internal/buildinfo"
)

// runCommand 处理不需要加载业务配置的本地子命令。
func runCommand(args []string, stdout, stderr io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "version":
		if len(args) != 1 {
			_, _ = fmt.Fprintln(stderr, "用法: limen version")
			return 2, true
		}
		if err := buildinfo.WriteJSON(stdout); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1, true
		}
		return 0, true
	case "healthcheck":
		if len(args) != 1 {
			_, _ = fmt.Fprintln(stderr, "用法: limen healthcheck")
			return 2, true
		}
		if err := runHealthcheck(context.Background(), os.Getenv("LIMEN_HEALTH_URL"), os.Getenv("LIMEN_ADDR"), http.DefaultClient); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1, true
		}
		_, _ = fmt.Fprintln(stdout, "ok")
		return 0, true
	case "demo":
		if len(args) != 1 {
			_, _ = fmt.Fprintln(stderr, "用法: limen demo")
			return 2, true
		}
		if err := runDemo(stdout); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1, true
		}
		return 0, true
	case "validate":
		if err := runValidate(args[1:], stdout, stderr); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1, true
		}
		return 0, true
	case "explain":
		if err := runExplain(args[1:], stdout, stderr); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1, true
		}
		return 0, true
	default:
		_, _ = fmt.Fprintln(stderr, "未知命令:", args[0])
		return 2, true
	}
}

// runHealthcheck 请求本地就绪接口，供容器和运维脚本使用。
func runHealthcheck(ctx context.Context, explicitURL, addr string, client *http.Client) error {
	target, err := healthURL(explicitURL, addr)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("创建健康检查请求失败: %w", err)
	}
	if client == nil {
		client = http.DefaultClient
	}
	checkClient := *client
	if checkClient.Timeout == 0 {
		checkClient.Timeout = 2 * time.Second
	}
	response, err := checkClient.Do(request)
	if err != nil {
		return fmt.Errorf("健康检查请求失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("服务未就绪: HTTP %d", response.StatusCode)
	}
	return nil
}

// healthURL 将监听地址转换为容器内可访问的本地健康检查地址。
func healthURL(explicitURL, addr string) (string, error) {
	if explicitURL != "" {
		return validateHealthURL(explicitURL)
	}
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := splitListenAddress(addr)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return "http://" + netJoinHostPort(host, port) + "/readyz", nil
}

// validateHealthURL 限制显式健康地址为 HTTP(S) URL。
func validateHealthURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("LIMEN_HEALTH_URL 必须是绝对 HTTP(S) URL")
	}
	if parsed.User != nil {
		return "", errors.New("LIMEN_HEALTH_URL 不得包含用户信息")
	}
	return parsed.String(), nil
}

// splitListenAddress 解析 host:port，同时兼容纯端口和 IPv6 监听地址。
func splitListenAddress(addr string) (string, string, error) {
	if strings.HasPrefix(addr, ":") {
		if addr[1:] == "" {
			return "", "8080", nil
		}
		return "", addr[1:], nil
	}
	if strings.HasPrefix(addr, "[") {
		closeBracket := strings.Index(addr, "]")
		if closeBracket < 0 || closeBracket+2 > len(addr) || addr[closeBracket+1] != ':' {
			return "", "", fmt.Errorf("LIMEN_ADDR 地址无效: %s", addr)
		}
		return addr[1:closeBracket], addr[closeBracket+2:], nil
	}
	parts := strings.Split(addr, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("LIMEN_ADDR 地址无效: %s", addr)
	}
	return parts[0], parts[1], nil
}

// netJoinHostPort 只在健康检查 URL 中拼接已校验的监听主机和端口。
func netJoinHostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}
