package redact

import (
	"strings"
	"sync"
	"testing"
)

func TestAddLiteralRedactsAfterConstruction(t *testing.T) {
	r := New()
	secret := "tok-addliteral-1234567890"
	if got := r.Redact("x " + secret + " y"); !strings.Contains(got, secret) {
		t.Fatalf("literal redacted before registration: %q", got)
	}
	r.AddLiteral(secret)
	if got := r.Redact("x " + secret + " y"); strings.Contains(got, secret) {
		t.Fatalf("literal survived redaction after AddLiteral: %q", got)
	}
	if !r.ContainsSecret("prefix " + secret) {
		t.Fatal("ContainsSecret false after AddLiteral")
	}
}

func TestAddLiteralIgnoresShortAndDuplicate(t *testing.T) {
	r := New()
	r.AddLiteral("shrt")
	if r.ContainsSecret("shrt") {
		t.Fatal("short literal must be ignored")
	}
	secret := "tok-duplicate-1234567890" // planted test fixture; gitleaks:allow
	r.AddLiteral(secret, secret)
	r.AddLiteral(secret)
	if got := r.Redact(secret); strings.Contains(got, secret) {
		t.Fatalf("duplicate registration broke redaction: %q", got)
	}
}

func TestAddLiteralConcurrent(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		s := strings.Repeat("abcdefgh", 2) + string(rune('a'+i))
		go func() { defer wg.Done(); r.AddLiteral(s) }()
		go func() { defer wg.Done(); _ = r.Redact("x " + s + " y") }()
	}
	wg.Wait()
	for i := 0; i < 8; i++ {
		s := strings.Repeat("abcdefgh", 2) + string(rune('a'+i))
		if got := r.Redact(s); strings.Contains(got, s) {
			t.Fatalf("literal %q survived after concurrent adds", s)
		}
	}
}
