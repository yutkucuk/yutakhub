package main

import (
	"bytes"
	"testing"
)

func TestLockRoundTrip(t *testing.T) {
	plain := append([]byte("PK\x03\x04"), bytes.Repeat([]byte("lampwarden"), 5000)...)
	code, err := newCode()
	if err != nil || len(normCode(code)) != 20 {
		t.Fatalf("code %q %v", code, err)
	}
	sealed, l, err := lockSeal(plain, code)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("lampwarden")) {
		t.Fatal("the package is readable")
	}
	// typed in lower case, with spaces instead of dashes: the same code
	typed := " " + string(bytes.ToLower(bytes.ReplaceAll([]byte(code), []byte("-"), []byte(" ")))) + " "
	if !l.matches(typed) || !l.matches(code) {
		t.Fatal("the right code is not recognised")
	}
	got, err := lockOpen(sealed, typed)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip: %v", err)
	}
	other, _ := newCode()
	if l.matches(other) || l.matches("") {
		t.Fatal("a wrong code passes the check")
	}
	if _, err := lockOpen(sealed, other); err != errBadKey {
		t.Fatalf("wrong code: %v", err)
	}
	bad := append([]byte(nil), sealed...)
	bad[len(bad)/2] ^= 1
	if _, err := lockOpen(bad, code); err != errBadKey {
		t.Fatalf("tampered file: %v", err)
	}
	bad = append([]byte(nil), sealed...)
	bad[10] ^= 1 // the salt in the header
	if _, err := lockOpen(bad, code); err != errBadKey {
		t.Fatalf("tampered header: %v", err)
	}
	if _, _, err := lockSeal(plain, "short"); err == nil {
		t.Fatal("a short code was accepted")
	}
}

// PBKDF2-HMAC-SHA256 against the published test vector (RFC 7914, section 11).
func TestPBKDF2(t *testing.T) {
	got := pbkdf2SHA256([]byte("passwd"), []byte("salt"), 1, 64)
	want := "55ac046e56e3089fec1691c22544b605f94185216dde0465e68b9d57c20dacbc49ca9cccf179b645991664b39d77ef317c71b845b1e30bd509112041d3a19783"
	if hexOf(got) != want {
		t.Fatalf("pbkdf2: %s", hexOf(got))
	}
}

func hexOf(b []byte) string {
	const d = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, v := range b {
		out = append(out, d[v>>4], d[v&15])
	}
	return string(out)
}
