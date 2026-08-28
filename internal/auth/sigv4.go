package auth

// Hand-written AWS Signature Version 4 (stdlib only): canonical
// request -> string to sign -> derived key -> signature. Proven byte-exact
// against the published AWS test vectors in sigv4_test.go. The secret key
// travels as []byte so intermediates can be zeroed; derived keys are HMAC
// outputs, wiped after use.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const sigv4Algorithm = "AWS4-HMAC-SHA256"

// signV4 signs req in place: sets X-Amz-Date, X-Amz-Security-Token (when a
// session token exists), and Authorization. payloadHash is the lowercase
// hex SHA-256 of the request body. The caller owns zeroing secret/session.
func signV4(req *http.Request, payloadHash, accessKeyID string, secret, session []byte, region, service string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	date := amzDate[:8]
	req.Header.Set("X-Amz-Date", amzDate)
	if len(session) > 0 {
		req.Header.Set("X-Amz-Security-Token", string(session))
	}
	canonical, signedHeaders := canonicalRequest(req, payloadHash)
	scope := date + "/" + region + "/" + service + "/aws4_request"
	sts := stringToSign(amzDate, scope, canonical)
	key := deriveSigningKey(secret, date, region, service)
	sig := hex.EncodeToString(hmacSHA256(key, []byte(sts)))
	zeroBytes(key)
	req.Header.Set("Authorization", sigv4Algorithm+
		" Credential="+accessKeyID+"/"+scope+
		", SignedHeaders="+signedHeaders+
		", Signature="+sig)
}

// canonicalRequest renders the request in AWS canonical form and returns it
// with the signed-headers list. Every header present on the request plus
// Host is signed.
func canonicalRequest(req *http.Request, payloadHash string) (canonical, signedHeaders string) {
	names := make([]string, 0, len(req.Header)+1)
	names = append(names, "host")
	for name := range req.Header {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	var headers strings.Builder
	for _, name := range names {
		value := req.Host
		if name != "host" {
			value = collapseSpaces(strings.Join(req.Header.Values(name), ","))
		} else if value == "" {
			value = req.URL.Host
		}
		headers.WriteString(name + ":" + value + "\n")
	}
	signedHeaders = strings.Join(names, ";")
	canonical = strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery(req.URL.Query()),
		headers.String(),
		signedHeaders,
		payloadHash,
	}, "\n")
	return canonical, signedHeaders
}

func stringToSign(amzDate, scope, canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return sigv4Algorithm + "\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
}

// deriveSigningKey walks the HMAC chain AWS4+secret -> date -> region ->
// service -> aws4_request. The caller zeroes the returned key.
func deriveSigningKey(secret []byte, date, region, service string) []byte {
	seed := make([]byte, 0, len(secret)+4)
	seed = append(seed, "AWS4"...)
	seed = append(seed, secret...)
	key := hmacSHA256(seed, []byte(date))
	zeroBytes(seed)
	for _, part := range []string{region, service, "aws4_request"} {
		next := hmacSHA256(key, []byte(part))
		zeroBytes(key)
		key = next
	}
	return key
}

func hmacSHA256(key, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// canonicalURI strict-encodes the wire path: every byte outside the RFC
// 3986 unreserved set is percent-encoded (including "%", which re-encodes
// path segments already encoded on the wire — AWS's double-encoding rule
// for every service except S3). "/" separators pass through.
func canonicalURI(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		return "/"
	}
	return strictEncode(path, true)
}

// canonicalQuery sorts parameters by key then value, strict-encoded with
// %20 for space.
func canonicalQuery(values url.Values) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), values[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, strictEncode(k, false)+"="+strictEncode(v, false))
		}
	}
	return strings.Join(parts, "&")
}

// strictEncode percent-encodes every byte outside unreserved
// (A-Z a-z 0-9 - . _ ~); keepSlash passes "/" through for paths.
func strictEncode(s string, keepSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~',
			keepSlash && c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xf])
		}
	}
	return b.String()
}

// collapseSpaces trims and collapses runs of spaces, per the canonical
// header-value rule.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
