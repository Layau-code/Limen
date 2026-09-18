package provider

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSecureHTTPClientRejectsUnallowlistedEndpoint(t *testing.T) {
	client := NewSecureHTTPClient(HTTPClientOptions{AllowedEndpoints: []string{"https://api.example"}})
	request := httptest.NewRequest(http.MethodGet, "https://other.example", nil)
	_, err := client.Do(request)
	if err == nil {
		t.Fatal("expected endpoint rejection")
	}
}

func TestSecureHTTPClientDisablesRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "final")
	}))
	defer server.Close()
	client := NewSecureHTTPClient(HTTPClientOptions{AllowHTTP: true, AllowPrivateIPs: true, AllowedEndpoints: []string{server.URL}})
	response, err := client.Get(server.URL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestSecureHTTPClientIgnoresEnvironmentProxy(t *testing.T) {
	var proxyHits atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		_, _ = io.WriteString(w, "proxy")
	}))
	defer proxy.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "target")
	}))
	defer target.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	client := NewSecureHTTPClient(HTTPClientOptions{AllowHTTP: true, AllowPrivateIPs: true, AllowedEndpoints: []string{target.URL}})
	response, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "target" || proxyHits.Load() != 0 {
		t.Fatalf("body=%q proxy_hits=%d", body, proxyHits.Load())
	}
}

func TestSafeDialerRejectsPrivateAddresses(t *testing.T) {
	dial := safeDialer(func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}, false)
	if _, err := dial(context.Background(), "tcp", "provider.example:443"); err == nil {
		t.Fatal("expected private address rejection")
	}
}

func TestSafeDialerRejectsMetadataAddress(t *testing.T) {
	dial := safeDialer(func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("169.254.169.254")}, nil
	}, false)
	if _, err := dial(context.Background(), "tcp", "metadata.example:443"); err == nil {
		t.Fatal("expected metadata address rejection")
	}
}

func TestEndpointIDIncludesProviderPath(t *testing.T) {
	first, err := EndpointIDForBaseURL("https://api.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := EndpointIDForBaseURL("https://api.example/other")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "endpoint:") {
		t.Fatalf("endpoint ids = %q, %q", first, second)
	}
}

func TestEndpointForBaseURLRejectsUserInfo(t *testing.T) {
	if _, err := EndpointForBaseURL("https://user:secret@api.example/v1"); err == nil {
		t.Fatal("endpoint helper accepted URL user info")
	}
}
