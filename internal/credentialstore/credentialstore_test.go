package credentialstore

import (
	"context"
	"testing"
)

func TestMemoryStoreEncryptsAndRotatesCredentials(t *testing.T) {
	vault, err := NewVault([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore(vault)
	first, err := store.Rotate(context.Background(), "tenant-a", "openai", "endpoint-a", "secret-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Rotate(context.Background(), "tenant-a", "openai", "endpoint-a", "secret-two")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || !second.Active {
		t.Fatalf("records = %+v %+v", first, second)
	}
	secret, record, err := store.Resolve(context.Background(), "tenant-a", "openai", "endpoint-a")
	if err != nil || string(secret) != "secret-two" || record.ID != second.ID {
		t.Fatalf("resolve = %q %+v, err=%v", secret, record, err)
	}
	if _, _, err := store.Resolve(context.Background(), "tenant-b", "openai", "endpoint-a"); err != ErrNotFound {
		t.Fatalf("cross-tenant resolve error = %v", err)
	}
	if err := store.Revoke(context.Background(), "tenant-a", "openai", "endpoint-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Resolve(context.Background(), "tenant-a", "openai", "endpoint-a"); err != ErrNotFound {
		t.Fatalf("revoked resolve error = %v", err)
	}
}

func TestVaultBindsCiphertextToEndpoint(t *testing.T) {
	vault, err := NewVault([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := vault.Encrypt("tenant-a", "openai", "endpoint-a", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Decrypt("tenant-a", "openai", "endpoint-b", ciphertext); err == nil {
		t.Fatal("expected endpoint binding error")
	}
}

func TestParseMasterKeySupportsHexAndBase64(t *testing.T) {
	raw := "01234567890123456789012345678901"
	hexKey := "3031323334353637383930313233343536373839303132333435363738393031"
	parsed, err := ParseMasterKey(hexKey)
	if err != nil || string(parsed) != raw {
		t.Fatalf("hex key = %q, err=%v", parsed, err)
	}
	parsed, err = ParseMasterKey("MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=")
	if err != nil || string(parsed) != raw {
		t.Fatalf("base64 key = %q, err=%v", parsed, err)
	}
}
