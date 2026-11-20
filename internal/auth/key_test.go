package auth

import (
	"strings"
	"testing"
)

func TestGenerateAPIKeyShape(t *testing.T) {
	creds, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(creds.Raw, KeyPrefix) {
		t.Fatalf("key %q missing prefix", creds.Raw)
	}
	if len(creds.Raw) != len(KeyPrefix)+43 {
		t.Fatalf("key length = %d", len(creds.Raw))
	}
	if creds.Prefix != KeyPrefix+creds.Raw[len(KeyPrefix):len(KeyPrefix)+prefixSecretLen] {
		t.Fatalf("prefix = %q", creds.Prefix)
	}
	if len(creds.Hash) != 64 || !Verify(creds.Raw, creds.Hash) {
		t.Fatal("hash/verify failed")
	}
	other, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if creds.Raw == other.Raw || creds.Prefix == other.Prefix {
		t.Fatal("generated keys/prefixes collided")
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	creds, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if Verify(creds.Raw[:len(creds.Raw)-1]+"x", creds.Hash) {
		t.Fatal("tampered key accepted")
	}
	if Verify("garbage", creds.Hash) || Verify(creds.Raw, "deadbeef") || Verify(creds.Raw, "") {
		t.Fatal("invalid input accepted")
	}
}
