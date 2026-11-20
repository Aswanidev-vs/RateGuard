package auth

import "testing"

func TestAdminVerifier(t *testing.T) {
	if _, err := NewAdminVerifier("short"); err == nil {
		t.Fatal("short key accepted")
	}
	v, err := NewAdminVerifier("a-very-long-random-admin-key")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Verify("a-very-long-random-admin-key") {
		t.Fatal("correct key rejected")
	}
	if v.Verify("a-very-long-random-admin-kex") || v.Verify("") {
		t.Fatal("wrong key accepted")
	}
	var nilVerifier *AdminVerifier
	if nilVerifier.Verify("a-very-long-random-admin-key") {
		t.Fatal("nil verifier accepted a key")
	}
}
