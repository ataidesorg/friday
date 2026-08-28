package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gcpResolver(t *testing.T, spy *spyRegistrar, getenv map[string]string) *Resolver {
	t.Helper()
	return NewResolver(spy, envOf(nil),
		WithGetenv(func(k string) string { return getenv[k] }),
		WithKeyringLookup(func(context.Context, string, string) (string, bool, error) {
			return "", false, ErrKeyringUnavailable
		}),
		WithExec(func(context.Context, []string, string) ([]byte, error) {
			t.Fatal("gcp chain must not exec")
			return nil, nil
		}),
		WithNow(func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) }),
	)
}

func testRSAKeyPEM(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// verifyJWT checks the assertion's signature against pub and returns claims.
func verifyJWT(t *testing.T, assertion string, pub *rsa.PublicKey) map[string]any {
	t.Helper()
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d parts", len(parts))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("JWT signature invalid: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestGCPServiceAccountGrant(t *testing.T) {
	key, pemStr := testRSAKeyPEM(t)
	var gotAssertion, gotGrant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		gotGrant = r.PostForm.Get("grant_type")
		gotAssertion = r.PostForm.Get("assertion")
		fmt.Fprint(w, `{"access_token":"spy-gcp-sa-bearer-1","expires_in":3600}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cred := map[string]string{
		"type": "service_account", "client_email": "spy@proj.iam.gserviceaccount.com",
		"private_key": pemStr, "token_uri": srv.URL,
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sa.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	spy := &spyRegistrar{}
	r := gcpResolver(t, spy, map[string]string{"VERTEX_CREDENTIALS_PATH": path})
	c, err := r.GCPBearer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Zero()
	if got := string(c.Value()); got != "spy-gcp-sa-bearer-1" {
		t.Fatalf("bearer = %q", got)
	}
	if gotGrant != gcpJWTGrant {
		t.Errorf("grant_type = %q", gotGrant)
	}
	claims := verifyJWT(t, gotAssertion, &key.PublicKey)
	if claims["iss"] != "spy@proj.iam.gserviceaccount.com" || claims["aud"] != srv.URL || claims["scope"] != gcpScope {
		t.Errorf("claims = %v", claims)
	}
	if exp, iat := claims["exp"].(float64), claims["iat"].(float64); exp-iat != 3600 {
		t.Errorf("exp-iat = %v", exp-iat)
	}
	for _, want := range []string{pemStr, "spy-gcp-sa-bearer-1"} {
		if !spy.saw(want) {
			t.Errorf("secret material not registered with redact (len %d)", len(want))
		}
	}
}

func TestGCPAuthorizedUserRefresh(t *testing.T) {
	var gotForm map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		gotForm = map[string]string{
			"grant_type": r.PostForm.Get("grant_type"), "client_id": r.PostForm.Get("client_id"),
			"client_secret": r.PostForm.Get("client_secret"), "refresh_token": r.PostForm.Get("refresh_token"),
		}
		fmt.Fprint(w, `{"access_token":"spy-gcp-adc-bearer-1","expires_in":3599}`)
	}))
	defer srv.Close()

	home := t.TempDir()
	adcDir := filepath.Join(home, ".config", "gcloud")
	if err := os.MkdirAll(adcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	adc := fmt.Sprintf(`{"type":"authorized_user","client_id":"spy-client","client_secret":"spy-gcp-client-secret-1","refresh_token":"spy-gcp-refresh-1","token_uri":%q}`, srv.URL) // gitleaks:allow
	if err := os.WriteFile(filepath.Join(adcDir, "application_default_credentials.json"), []byte(adc), 0o600); err != nil {
		t.Fatal(err)
	}
	spy := &spyRegistrar{}
	r := gcpResolver(t, spy, map[string]string{"HOME": home})
	c, err := r.GCPBearer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Zero()
	if got := string(c.Value()); got != "spy-gcp-adc-bearer-1" {
		t.Fatalf("bearer = %q", got)
	}
	want := map[string]string{ //nolint:gosec // planted test values
		"grant_type": "refresh_token", "client_id": "spy-client",
		"client_secret": "spy-gcp-client-secret-1", "refresh_token": "spy-gcp-refresh-1",
	}
	for k, v := range want {
		if gotForm[k] != v {
			t.Errorf("form[%s] = %q, want %q", k, gotForm[k], v)
		}
	}
	for _, s := range []string{"spy-gcp-client-secret-1", "spy-gcp-refresh-1", "spy-gcp-adc-bearer-1"} {
		if !spy.saw(s) {
			t.Errorf("%q not registered with redact", s)
		}
	}
}

func TestGCPMalformedAndUnsupportedFailClosed(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := gcpResolver(t, &spyRegistrar{}, map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": bad})
	_, err := r.GCPBearer(context.Background())
	if err == nil || !strings.Contains(err.Error(), "malformed JSON") || !strings.Contains(err.Error(), bad) {
		t.Fatalf("malformed err = %v", err)
	}

	ext := filepath.Join(dir, "ext.json")
	if err := os.WriteFile(ext, []byte(`{"type":"external_account"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r = gcpResolver(t, &spyRegistrar{}, map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": ext})
	_, err = r.GCPBearer(context.Background())
	if err == nil || !strings.Contains(err.Error(), `unsupported type "external_account"`) {
		t.Fatalf("unsupported err = %v", err)
	}

	partial := filepath.Join(dir, "partial.json")
	if err := os.WriteFile(partial, []byte(`{"type":"service_account","client_email":"x@y"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r = gcpResolver(t, &spyRegistrar{}, map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": partial})
	_, err = r.GCPBearer(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to guess") {
		t.Fatalf("partial err = %v", err)
	}
}

func TestGCPMissListsEveryLocation(t *testing.T) {
	r := gcpResolver(t, &spyRegistrar{}, map[string]string{"HOME": t.TempDir()})
	_, err := r.GCPBearer(context.Background())
	var miss *ErrNoCredential
	if !errors.As(err, &miss) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
	for _, want := range []string{
		"VERTEX_CREDENTIALS_PATH", "GOOGLE_APPLICATION_CREDENTIALS",
		"application_default_credentials.json", "gcloud auth application-default login",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("miss %q lacks %q", err.Error(), want)
		}
	}
}
