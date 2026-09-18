package store

import (
	"strings"
	"testing"
)

func TestGenerateAPIKeyUsesLimenFormatAndHighEntropySecret(t *testing.T) {
	first, firstPrefix, err := generateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	second, secondPrefix, err := generateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "lmn_live_"+firstPrefix+"_") || !strings.HasPrefix(second, "lmn_live_"+secondPrefix+"_") || first == second || firstPrefix == secondPrefix {
		t.Fatalf("generated keys are invalid: first=%q second=%q", first, second)
	}
}
