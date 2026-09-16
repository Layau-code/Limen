package provider

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HTTPClientOptions 定义 Provider 出站请求的地址和网络安全边界。
type HTTPClientOptions struct {
	AllowedEndpoints []string
	AllowHTTP        bool
	AllowPrivateIPs  bool
	LookupIP         func(context.Context, string) ([]net.IP, error)
}

// NewSecureHTTPClient 创建禁用代理和重定向、并限制出站地址的 HTTP Client。
func NewSecureHTTPClient(options HTTPClientOptions) *http.Client {
	allowed := make(map[string]struct{}, len(options.AllowedEndpoints))
	for _, endpoint := range options.AllowedEndpoints {
		allowed[normalizeEndpoint(endpoint)] = struct{}{}
	}
	lookupIP := options.LookupIP
	if lookupIP == nil {
		lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
			addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			ips := make([]net.IP, 0, len(addresses))
			for _, address := range addresses {
				ips = append(ips, address.IP)
			}
			return ips, nil
		}
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.DialContext = safeDialer(lookupIP, options.AllowPrivateIPs)
	base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport := &secureRoundTripper{base: base, allowed: allowed, allowHTTP: options.AllowHTTP}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 0,
	}
}

// EndpointForBaseURL 将 Provider 根地址转换为 allowlist 使用的 host:port。
func EndpointForBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("invalid provider base URL")
	}
	return normalizeEndpoint(parsed.Scheme + "://" + parsed.Host), nil
}

type secureRoundTripper struct {
	base      http.RoundTripper
	allowed   map[string]struct{}
	allowHTTP bool
}

func (transport *secureRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL == nil || request.URL.Hostname() == "" {
		return nil, errors.New("provider URL has no host")
	}
	if request.URL.Scheme != "https" && !(transport.allowHTTP && request.URL.Scheme == "http") {
		return nil, errors.New("insecure provider URL is disabled")
	}
	if len(transport.allowed) > 0 {
		if _, ok := transport.allowed[normalizeEndpoint(request.URL.Scheme+"://"+request.URL.Host)]; !ok {
			return nil, fmt.Errorf("provider endpoint is not allowlisted: %s", request.URL.Host)
		}
	}
	return transport.base.RoundTrip(request)
}

func safeDialer(lookupIP func(context.Context, string) ([]net.IP, error), allowPrivate bool) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips := []net.IP{net.ParseIP(host)}
		if ips[0] == nil {
			ips, err = lookupIP(ctx, host)
			if err != nil {
				return nil, err
			}
		}
		for _, ip := range ips {
			if !allowPrivate && isPrivateAddress(ip) {
				continue
			}
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			err = dialErr
		}
		if err == nil {
			err = errors.New("no permitted provider address")
		}
		return nil, err
	}
}

func isPrivateAddress(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

func normalizeEndpoint(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return strings.ToLower(raw)
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = strconv.Itoa(443)
		} else if parsed.Scheme == "http" {
			port = strconv.Itoa(80)
		}
	}
	return host + ":" + port
}
