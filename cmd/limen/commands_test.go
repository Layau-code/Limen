package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthURLNormalizesListenAddresses(t *testing.T) {
	tests := map[string]string{
		":8080":          "http://127.0.0.1:8080/readyz",
		"0.0.0.0:8080":   "http://127.0.0.1:8080/readyz",
		"[::]:8080":      "http://127.0.0.1:8080/readyz",
		"127.0.0.1:9090": "http://127.0.0.1:9090/readyz",
	}
	for addr, want := range tests {
		got, err := healthURL("", addr)
		if err != nil || got != want {
			t.Fatalf("addr %q => %q, %v; want %q", addr, got, err, want)
		}
	}
}

func TestHealthcheckUsesExplicitURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := runHealthcheck(context.Background(), server.URL+"/readyz", "bad-address", server.Client()); err != nil {
		t.Fatal(err)
	}
}

func TestRunCommandVersionDoesNotLoadConfiguration(t *testing.T) {
	var stdout, stderr strings.Builder
	if code, handled := runCommand([]string{"version"}, &stdout, &stderr); !handled || code != 0 {
		t.Fatalf("code=%d handled=%t stderr=%q", code, handled, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"version":"dev"`) || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
