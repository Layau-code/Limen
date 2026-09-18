package auth

import "testing"

func TestStaticAuthenticatorReturnsTenantAndScopes(t *testing.T) {
	authenticator := NewStaticAuthenticator("secret", "tenant-1", []Scope{ScopeInference})
	principal, ok := authenticator.Authenticate("Bearer secret")
	if !ok || principal.TenantID != "tenant-1" || !principal.HasScope(ScopeInference) || principal.HasScope(ScopeRunsWrite) {
		t.Fatalf("principal=%+v ok=%v", principal, ok)
	}
}

func TestStaticAuthenticatorRejectsInvalidKey(t *testing.T) {
	authenticator := NewStaticAuthenticator("secret", "tenant-1", nil)
	for _, header := range []string{"Bearer wrong", "secret", "Bearer ", "Bearer secret ", "Basic secret"} {
		if _, ok := authenticator.Authenticate(header); ok {
			t.Fatalf("invalid header accepted: %q", header)
		}
	}
}

func TestStaticAuthenticatorCopiesScopes(t *testing.T) {
	authenticator := NewStaticAuthenticator("secret", "tenant-1", []Scope{ScopeInference})
	principal, ok := authenticator.Authenticate("Bearer secret")
	if !ok {
		t.Fatal("valid key rejected")
	}
	delete(principal.Scopes, ScopeInference)
	if next, ok := authenticator.Authenticate("Bearer secret"); !ok || !next.HasScope(ScopeInference) {
		t.Fatal("principal mutation changed authenticator")
	}
}

func TestValidatePublicPrefixRequiresFixedHex(t *testing.T) {
	if !ValidatePublicPrefix("0123456789abcdef") {
		t.Fatal("valid public prefix rejected")
	}
	for _, prefix := range []string{"public123", "0123456789abcdeg", "0123456789abcdef0"} {
		if ValidatePublicPrefix(prefix) {
			t.Fatalf("invalid public prefix accepted: %q", prefix)
		}
	}
}
