package auth

// The published AWS SigV4 example (IAM ListUsers, 20150830, us-east-1) and
// the signing-key derivation example from the AWS General Reference. Each
// intermediate is asserted byte-exact so a drift in any step names itself.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	vecAccessKey = "AKIDEXAMPLE"
	vecSecret    = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY" //nolint:gosec // gitleaks:allow — published AWS docs example
	vecRegion    = "us-east-1"
	vecService   = "iam"
)

func vecTime(t *testing.T) time.Time {
	t.Helper()
	ts, err := time.Parse("20060102T150405Z", "20150830T123600Z")
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func vecRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("X-Amz-Date", "20150830T123600Z")
	return req
}

func TestSigV4DeriveSigningKeyVector(t *testing.T) {
	key := deriveSigningKey([]byte(vecSecret), "20150830", vecRegion, vecService)
	const want = "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9"
	if got := hex.EncodeToString(key); got != want {
		t.Fatalf("derived key = %s, want %s", got, want)
	}
}

func TestSigV4CanonicalRequestVector(t *testing.T) {
	req := vecRequest(t)
	empty := sha256.Sum256(nil)
	canonical, signedHeaders := canonicalRequest(req, hex.EncodeToString(empty[:]))
	want := strings.Join([]string{
		"GET",
		"/",
		"Action=ListUsers&Version=2010-05-08",
		"content-type:application/x-www-form-urlencoded; charset=utf-8",
		"host:iam.amazonaws.com",
		"x-amz-date:20150830T123600Z",
		"",
		"content-type;host;x-amz-date",
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}, "\n")
	if canonical != want {
		t.Fatalf("canonical request:\n%q\nwant:\n%q", canonical, want)
	}
	if signedHeaders != "content-type;host;x-amz-date" {
		t.Fatalf("signed headers = %q", signedHeaders)
	}
	sum := sha256.Sum256([]byte(canonical))
	const wantHash = "f536975d06c0309214f805bb90ccff089219ecd68b2577efef23edd43b7e1a59"
	if got := hex.EncodeToString(sum[:]); got != wantHash {
		t.Fatalf("canonical hash = %s, want %s", got, wantHash)
	}
}

func TestSigV4StringToSignVector(t *testing.T) {
	got := stringToSign("20150830T123600Z", "20150830/us-east-1/iam/aws4_request",
		func() string {
			c, _ := canonicalRequest(vecRequest(t), emptyPayloadHash())
			return c
		}())
	want := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		"20150830T123600Z",
		"20150830/us-east-1/iam/aws4_request",
		"f536975d06c0309214f805bb90ccff089219ecd68b2577efef23edd43b7e1a59",
	}, "\n")
	if got != want {
		t.Fatalf("string to sign:\n%q\nwant:\n%q", got, want)
	}
}

func emptyPayloadHash() string {
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}

func TestSigV4FullSignatureVector(t *testing.T) {
	req := vecRequest(t)
	signV4(req, emptyPayloadHash(), vecAccessKey, []byte(vecSecret), nil, vecRegion, vecService, vecTime(t))
	const want = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-date, " +
		"Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("authorization:\n%q\nwant:\n%q", got, want)
	}
	if req.Header.Get("X-Amz-Security-Token") != "" {
		t.Fatal("no session token was given; header must be absent")
	}
}

func TestSigV4SessionTokenHeader(t *testing.T) {
	req := vecRequest(t)
	signV4(req, emptyPayloadHash(), vecAccessKey, []byte(vecSecret), []byte("spy-session-1"), vecRegion, vecService, vecTime(t))
	if got := req.Header.Get("X-Amz-Security-Token"); got != "spy-session-1" {
		t.Fatalf("security token header = %q", got)
	}
	// The token header itself is signed: it must appear in SignedHeaders.
	if auth := req.Header.Get("Authorization"); !strings.Contains(auth, "x-amz-security-token") {
		t.Fatalf("authorization does not sign the token header: %q", auth)
	}
}

func TestSigV4PathAndQueryEncoding(t *testing.T) {
	// Bedrock model ids carry ":" in the path; canonical form re-encodes
	// the wire path strictly (AWS double-encoding rule for non-S3).
	req, err := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-3-5-sonnet-20240620-v1:0/converse", nil)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := canonicalRequest(req, emptyPayloadHash())
	lines := strings.Split(canonical, "\n")
	if lines[1] != "/model/anthropic.claude-3-5-sonnet-20240620-v1%3A0/converse" {
		t.Fatalf("canonical URI = %q", lines[1])
	}
	if got := strictEncode("a b~c%", false); got != "a%20b~c%25" {
		t.Fatalf("strictEncode = %q", got)
	}
}
