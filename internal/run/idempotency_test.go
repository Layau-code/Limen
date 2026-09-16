package run

import "testing"

func TestHashRequestIgnoresAuthorizationAndSortsHeaders(t *testing.T) {
	first, err := HashRequest("tenant-1", "/v1/chat/completions", "key-1", []byte(`{"model":"auto"}`), map[string]string{
		"X-Limen-Data-Class": "internal",
		"Authorization":      "Bearer first-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashRequest("tenant-1", "/v1/chat/completions", "key-1", []byte(`{"model":"auto"}`), map[string]string{
		"authorization":      "Bearer another-secret",
		"x-limen-data-class": "internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("hashes differ: %q != %q", first, second)
	}
}

func TestHashRequestCanonicalizesJSONBody(t *testing.T) {
	first, err := HashRequest("tenant", "/endpoint", "key", []byte(`{"model":"auto","stream":false}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashRequest("tenant", "/endpoint", "key", []byte("{\n  \"stream\": false, \"model\": \"auto\"\n}"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("canonical hashes differ: %q != %q", first, second)
	}
}

func TestHashRequestRequiresIdentityFields(t *testing.T) {
	for _, input := range [][3]string{{"", "/v1/chat/completions", "key"}, {"tenant", "", "key"}, {"tenant", "/v1/chat/completions", ""}} {
		if _, err := HashRequest(input[0], input[1], input[2], nil, nil); err == nil {
			t.Fatalf("accepted invalid identity: %v", input)
		}
	}
}
