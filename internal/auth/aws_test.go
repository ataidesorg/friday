package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// awsResolver builds a hermetic resolver: env map feeds credentials,
// getenv map feeds paths/profile, nothing touches keyring, exec, or clock.
func awsResolver(t *testing.T, spy *spyRegistrar, env, getenv map[string]string) *Resolver {
	t.Helper()
	return NewResolver(spy, envOf(env),
		WithGetenv(func(k string) string { return getenv[k] }),
		WithKeyringLookup(func(context.Context, string, string) (string, bool, error) {
			return "", false, ErrKeyringUnavailable
		}),
		WithExec(func(context.Context, []string, string) ([]byte, error) {
			t.Fatal("aws chain must not exec")
			return nil, nil
		}),
		WithNow(func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) }),
	)
}

func TestAWSChainEnvFirst(t *testing.T) {
	spy := &spyRegistrar{}
	r := awsResolver(t, spy, map[string]string{ //nolint:gosec // planted test values
		"AWS_ACCESS_KEY_ID":     "AKIASPYENV1",
		"AWS_SECRET_ACCESS_KEY": "spy-aws-secret-env-1", // gitleaks:allow
		"AWS_SESSION_TOKEN":     "spy-aws-session-env-1",
	}, nil)
	cred, err := r.AWSCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessKeyID() != "AKIASPYENV1" {
		t.Fatalf("access key = %q", cred.AccessKeyID())
	}
	req, _ := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/m/converse", nil)
	cred.SignRequest(req, emptyPayloadHash(), "us-east-1", "bedrock", time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC))
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIASPYENV1/20260824/us-east-1/bedrock/aws4_request") {
		t.Fatalf("authorization = %q", auth)
	}
	if req.Header.Get("X-Amz-Security-Token") != "spy-aws-session-env-1" {
		t.Fatal("session token header missing")
	}
	for _, want := range []string{"spy-aws-secret-env-1", "spy-aws-session-env-1"} {
		if !spy.saw(want) {
			t.Errorf("%q not registered with redact", want)
		}
	}
	cred.Zero()
	if len(cred.secret) != 0 || len(cred.session) != 0 {
		t.Fatal("Zero left secret bytes")
	}
}

func TestAWSChainEnvPartialPairFailsClosed(t *testing.T) {
	r := awsResolver(t, &spyRegistrar{}, map[string]string{"AWS_ACCESS_KEY_ID": "AKIAONLY"}, nil)
	_, err := r.AWSCredentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), "AWS_SECRET_ACCESS_KEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestAWSChainSharedCredentialsFile(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".aws")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ini := "; comment\n[default]\naws_access_key_id = AKIADEFAULT\naws_secret_access_key = spy-aws-file-default-1\n\n[work]\naws_access_key_id = AKIAWORK\naws_secret_access_key = spy-aws-file-work-1\naws_session_token = spy-aws-file-session-1\n"
	if err := os.WriteFile(filepath.Join(dir, "credentials"), []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	spy := &spyRegistrar{}
	r := awsResolver(t, spy, nil, map[string]string{"HOME": home, "AWS_PROFILE": "work"})
	cred, err := r.AWSCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cred.Zero()
	if cred.AccessKeyID() != "AKIAWORK" || string(cred.session) != "spy-aws-file-session-1" {
		t.Fatalf("resolved %q session %q, want the work profile", cred.AccessKeyID(), cred.session)
	}
	if !spy.saw("spy-aws-file-work-1") {
		t.Error("file secret not registered with redact")
	}

	// Partial pair fails closed, never a half credential.
	bad := filepath.Join(t.TempDir(), "creds")
	if err := os.WriteFile(bad, []byte("[default]\naws_access_key_id = AKIAHALF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = awsResolver(t, &spyRegistrar{}, nil, map[string]string{"AWS_SHARED_CREDENTIALS_FILE": bad})
	_, err = r.AWSCredentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), "partial key pair") {
		t.Fatalf("partial pair err = %v", err)
	}
}

func TestAWSChainConfigFileProfilePrefix(t *testing.T) {
	home := t.TempDir()
	conf := filepath.Join(t.TempDir(), "config")
	ini := "[profile work]\naws_access_key_id = AKIACONF\naws_secret_access_key = spy-aws-conf-1\n"
	if err := os.WriteFile(conf, []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	r := awsResolver(t, &spyRegistrar{}, nil, map[string]string{
		"HOME": home, "AWS_PROFILE": "work", "AWS_CONFIG_FILE": conf,
	})
	cred, err := r.AWSCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cred.Zero()
	if cred.AccessKeyID() != "AKIACONF" {
		t.Fatalf("access key = %q", cred.AccessKeyID())
	}
}

func TestAWSChainCLICache(t *testing.T) {
	home := t.TempDir()
	cache := filepath.Join(home, ".aws", "cli", "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	expired := `{"Credentials":{"AccessKeyId":"AKIAOLD","SecretAccessKey":"spy-aws-cache-old-1","SessionToken":"s","Expiration":"2026-08-24T11:00:00Z"}}`
	valid := `{"Credentials":{"AccessKeyId":"AKIACACHE","SecretAccessKey":"spy-aws-cache-1","SessionToken":"spy-aws-cache-session-1","Expiration":"2026-08-25T12:00:00Z"}}`
	if err := os.WriteFile(filepath.Join(cache, "a-expired.json"), []byte(expired), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "b-valid.json"), []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	spy := &spyRegistrar{}
	r := awsResolver(t, spy, nil, map[string]string{"HOME": home})
	cred, err := r.AWSCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cred.Zero()
	if cred.AccessKeyID() != "AKIACACHE" {
		t.Fatalf("access key = %q, want the unexpired cache entry", cred.AccessKeyID())
	}
	if !spy.saw("spy-aws-cache-session-1") {
		t.Error("cache session token not registered with redact")
	}

	if err := os.WriteFile(filepath.Join(cache, "c-bad.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = r.AWSCredentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), "malformed JSON") || !strings.Contains(err.Error(), "c-bad.json") {
		t.Fatalf("malformed cache err = %v", err)
	}
}

func TestAWSChainIMDSOffByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("IMDS must not be contacted without AWS_EC2_METADATA_SERVICE_ENDPOINT")
	}))
	defer srv.Close()
	r := awsResolver(t, &spyRegistrar{}, nil, map[string]string{"HOME": t.TempDir()})
	_, err := r.AWSCredentials(context.Background())
	var miss *ErrNoCredential
	if err == nil {
		t.Fatal("want a miss")
	}
	if !errors.As(err, &miss) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"AWS_ACCESS_KEY_ID", ".aws/credentials", "[default]", ".aws/config",
		"cli/cache", "AWS_EC2_METADATA_SERVICE_ENDPOINT",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("miss %q lacks %q", msg, want)
		}
	}
}

func TestAWSChainIMDSWhenConfigured(t *testing.T) {
	var sawTokenPut, sawTokenHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
			sawTokenPut = true
			_, _ = w.Write([]byte("spy-imds-token-1"))
		case r.URL.Path == "/latest/meta-data/iam/security-credentials/":
			sawTokenHeader = r.Header.Get("X-aws-ec2-metadata-token") == "spy-imds-token-1"
			_, _ = w.Write([]byte("spy-role\n"))
		case r.URL.Path == "/latest/meta-data/iam/security-credentials/spy-role":
			_, _ = w.Write([]byte(`{"AccessKeyId":"AKIAIMDS","SecretAccessKey":"spy-aws-imds-1","Token":"spy-aws-imds-session-1","Expiration":"2026-08-25T12:00:00Z"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	spy := &spyRegistrar{}
	r := awsResolver(t, spy, nil, map[string]string{
		"HOME": t.TempDir(), "AWS_EC2_METADATA_SERVICE_ENDPOINT": srv.URL,
	})
	cred, err := r.AWSCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cred.Zero()
	if cred.AccessKeyID() != "AKIAIMDS" || string(cred.session) != "spy-aws-imds-session-1" {
		t.Fatalf("resolved %q, want the IMDS role credential", cred.AccessKeyID())
	}
	if !sawTokenPut || !sawTokenHeader {
		t.Error("IMDSv2 token flow not exercised")
	}
	if !spy.saw("spy-aws-imds-1") {
		t.Error("IMDS secret not registered with redact")
	}
}
