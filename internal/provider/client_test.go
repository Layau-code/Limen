package provider

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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

func TestSafeDialerRejectsPrivateAddresses(t *testing.T) {
	dial := safeDialer(func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}, false)
	if _, err := dial(context.Background(), "tcp", "provider.example:443"); err == nil {
		t.Fatal("expected private address rejection")
	}
}
