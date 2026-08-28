package redact

import (
	"strings"
	"testing"
	"testing/quick"
)

// Secret-shaped inputs are assembled from fragments so no literal in this
// repository ever matches a secret scanner rule.
var (
	rep         = strings.Repeat
	openaiKey   = "sk-proj-" + rep("x", 30)
	ghpToken    = "ghp_" + rep("A", 36)
	ghPAT       = "github_pat_" + rep("A", 22) + "_" + rep("b", 30)
	slackToken  = "xoxb-" + rep("1", 10) + "-" + rep("a", 10)
	assignValue = rep("a", 8) + rep("1", 8)
	awsKey      = "AKIA" + rep("A", 16)
	googleKey   = "AIza" + rep("S", 35)
	jwtPayload  = "eyJ" + rep("z", 20)
	jwtToken    = "eyJ" + rep("h", 20) + "." + jwtPayload + "." + rep("s", 16)
)

func TestBuiltinPatterns(t *testing.T) {
	r := New()
	cases := map[string]struct{ in, mustNotContain, mustContain string }{
		"openai key":      {"key is " + openaiKey, openaiKey, "[REDACTED:openai_key]"},
		"github token":    {ghpToken, "ghp_AAAAAAAAAA", "[REDACTED:github_token]"},
		"github pat":      {ghPAT, "github_pat_" + rep("A", 22), "[REDACTED:github_token]"},
		"slack token":     {slackToken, "xoxb-" + rep("1", 10), "[REDACTED:slack_token]"},
		"aws key":         {awsKey, awsKey, "[REDACTED:aws_access_key]"},
		"google key":      {googleKey, "AIza" + rep("S", 16), "[REDACTED:google_api_key]"},
		"jwt":             {jwtToken, jwtPayload, "[REDACTED:jwt]"},
		"bearer":          {"Authorization: Bearer " + rep("a", 26), rep("a", 26), "[REDACTED:bearer_token]"},
		"private key":     {"-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----", "MIIEow", "[REDACTED:private_key]"},
		"assignment eq":   {"OPENAI_API_KEY=" + assignValue, assignValue, "OPENAI_API_KEY=[REDACTED:assignment]"},
		"assignment yaml": {"password: hunter2hunter2", "hunter2hunter2", "password: [REDACTED:assignment]"},
		"assignment toml": {`api_key = "` + assignValue + `"`, assignValue, `api_key = "[REDACTED:assignment]"`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := r.Redact(tc.in)
			if strings.Contains(got, tc.mustNotContain) {
				t.Fatalf("secret survived redaction: %q", got)
			}
			if !strings.Contains(got, tc.mustContain) {
				t.Fatalf("expected marker %q in %q", tc.mustContain, got)
			}
			if !r.ContainsSecret(tc.in) {
				t.Fatalf("ContainsSecret false for %q", tc.in)
			}
		})
	}
}

func TestBenignTextUntouched(t *testing.T) {
	r := New()
	benign := []string{
		"",
		"hello world",
		"max_tokens = 4096",
		"episodic_days = 30",
		"the password field is required",
		"func NewTask(description string) error",
		"2026-08-21T18:02:00Z",
		"git status shows 3 files",
		"run `go test ./...` before committing",
	}
	for _, s := range benign {
		if got := r.Redact(s); got != s {
			t.Errorf("benign text altered: %q -> %q", s, got)
		}
		if r.ContainsSecret(s) {
			t.Errorf("ContainsSecret true for benign %q", s)
		}
	}
}

func TestLiteralSecrets(t *testing.T) {
	r := New("correct-horse-battery", "short")
	got := r.Redact("token was correct-horse-battery and short")
	if strings.Contains(got, "correct-horse-battery") {
		t.Fatalf("literal secret survived: %q", got)
	}
	if !strings.Contains(got, "short") {
		t.Fatalf("literal below MinLiteralLen must be ignored, got %q", got)
	}
	if !strings.Contains(got, "[REDACTED:literal]") {
		t.Fatalf("expected literal marker in %q", got)
	}
}

func TestLongestLiteralFirst(t *testing.T) {
	r := New("abcdefgh", "abcdefghijkl")
	got := r.Redact("x abcdefghijkl y")
	if got != "x [REDACTED:literal] y" {
		t.Fatalf("expected single marker, got %q", got)
	}
}

func TestRedactIsIdempotent(t *testing.T) {
	r := New("correct-horse-battery")
	in := "password=hunter2hunter2 " + openaiKey + " correct-horse-battery"
	once := r.Redact(in)
	if twice := r.Redact(once); twice != once {
		t.Fatalf("not idempotent:\n once=%q\ntwice=%q", once, twice)
	}
}

func TestLiteralNeverSurvivesProperty(t *testing.T) {
	prop := func(prefix, suffix string, raw [16]byte) bool {
		secret := secretFromBytes(raw)
		r := New(secret)
		out := r.Redact(prefix + secret + suffix)
		return !strings.Contains(out, secret)
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}

func FuzzLiteralNeverSurvives(f *testing.F) {
	f.Add("prefix ", " suffix", "abcdefgh12345678")
	f.Add("", "", "zzzzzzzz")
	f.Fuzz(func(t *testing.T, prefix, suffix, secret string) {
		if len(secret) < MinLiteralLen || len(secret) > 64 || !isLowerAlnum(secret) {
			t.Skip()
		}
		out := New(secret).Redact(prefix + secret + suffix)
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q survived in %q", secret, out)
		}
	})
}

// secretFromBytes maps random bytes onto [a-z0-9]{16}; that alphabet can never
// be a substring of a redaction marker, which keeps the property sound.
func secretFromBytes(raw [16]byte) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, len(raw))
	for i, c := range raw {
		b[i] = alphabet[int(c)%len(alphabet)]
	}
	return string(b)
}

func isLowerAlnum(s string) bool {
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
