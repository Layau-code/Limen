package store

import "testing"

func TestSplitAPIKey(t *testing.T) {
	prefix, token, ok := splitAPIKey("Bearer lmn_live_0123456789abcdef_secret456")
	if !ok || prefix != "0123456789abcdef" || token != "lmn_live_0123456789abcdef_secret456" {
		t.Fatalf("prefix=%q token=%q ok=%v", prefix, token, ok)
	}
	for _, header := range []string{"Bearer secret", "Bearer lmn_live__secret", "Bearer lmn_live_public_", "Bearer lmn_live_public123_secret456", "Basic lmn_live_public_secret"} {
		if _, _, ok := splitAPIKey(header); ok {
			t.Fatalf("invalid key accepted: %q", header)
		}
	}
}

func TestHMACDigestIsDeterministic(t *testing.T) {
	first := hmacDigest([]byte("secret"), "lmn_live_public_secret")
	second := hmacDigest([]byte("secret"), "lmn_live_public_secret")
	if string(first) != string(second) || len(first) != 32 {
		t.Fatalf("digests differ: %x %x", first, second)
	}
}
