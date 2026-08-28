package auth

// GCP credential chain for Vertex: an explicit service-account or
// authorized-user JSON file exchanged for a short-lived bearer. Order:
// VERTEX_CREDENTIALS_PATH, GOOGLE_APPLICATION_CREDENTIALS, then the gcloud
// ADC file. Never a metadata-server probe: Friday runs on workstations, and
// unsolicited network probes are off the table.

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
	"net/url"
	"os"
	"path/filepath"
)

const (
	gcpScope           = "https://www.googleapis.com/auth/cloud-platform"
	gcpDefaultTokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // OAuth token endpoint URL, not a credential
	gcpJWTGrant        = "urn:ietf:params:oauth:grant-type:jwt-bearer"
)

// gcpCredFile is the union of the two Google credential JSON shapes.
type gcpCredFile struct {
	Type         string `json:"type"`
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	TokenURI     string `json:"token_uri"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

// GCPBearer resolves a GCP access token for the Vertex OpenAI-compatible
// endpoint. The long-lived material (private key, refresh token) and the
// minted bearer are all registered with the redactor the moment they are
// read; the bearer lives in the returned Credential only.
func (r *Resolver) GCPBearer(ctx context.Context) (*Credential, error) {
	path, where := r.gcpCredPath()
	if path == "" {
		return nil, &ErrNoCredential{
			Source: "gcp chain",
			Where:  where,
			Hint:   "run `gcloud auth application-default login` or point GOOGLE_APPLICATION_CREDENTIALS at a service-account JSON",
		}
	}
	data, err := os.ReadFile(path) //nolint:gosec // path from the user's own env/HOME
	if err != nil {
		return nil, fmt.Errorf("gcp credentials %s: %w", path, err)
	}
	var f gcpCredFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("gcp credentials %s: malformed JSON", path)
	}
	switch f.Type {
	case "service_account":
		return r.gcpServiceAccountToken(ctx, path, f)
	case "authorized_user":
		return r.gcpAuthorizedUserToken(ctx, path, f)
	default:
		return nil, fmt.Errorf("gcp credentials %s: unsupported type %q (want service_account or authorized_user)", path, f.Type)
	}
}

// gcpCredPath walks the path chain; the second return names every location
// tried for the miss error.
func (r *Resolver) gcpCredPath() (path, where string) {
	adc := filepath.Join(r.getenv("HOME"), ".config", "gcloud", "application_default_credentials.json")
	where = "env VERTEX_CREDENTIALS_PATH, env GOOGLE_APPLICATION_CREDENTIALS, " + adc
	for _, env := range []string{"VERTEX_CREDENTIALS_PATH", "GOOGLE_APPLICATION_CREDENTIALS"} {
		if p := r.getenv(env); p != "" {
			return p, where
		}
	}
	if _, err := os.Stat(adc); err == nil {
		return adc, where
	}
	return "", where
}

func (r *Resolver) gcpServiceAccountToken(ctx context.Context, path string, f gcpCredFile) (*Credential, error) {
	if f.ClientEmail == "" || f.PrivateKey == "" || f.TokenURI == "" {
		return nil, fmt.Errorf("gcp credentials %s: service_account file missing client_email, private_key, or token_uri; refusing to guess", path)
	}
	r.register.AddLiteral(f.PrivateKey)
	key, err := parseRSAPrivateKey(f.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("gcp credentials %s: %w", path, err)
	}
	assertion, err := signJWTRS256(key, f.ClientEmail, gcpScope, f.TokenURI, r.now().Unix())
	if err != nil {
		return nil, fmt.Errorf("gcp credentials %s: sign JWT: %w", path, err)
	}
	tr, err := r.tokenRequest(ctx, f.TokenURI, url.Values{
		"grant_type": {gcpJWTGrant},
		"assertion":  {assertion},
	})
	if err != nil {
		return nil, fmt.Errorf("gcp token exchange (%s): %w", f.TokenURI, err)
	}
	return r.credential(tr.AccessToken), nil
}

func (r *Resolver) gcpAuthorizedUserToken(ctx context.Context, path string, f gcpCredFile) (*Credential, error) {
	if f.ClientID == "" || f.ClientSecret == "" || f.RefreshToken == "" {
		return nil, fmt.Errorf("gcp credentials %s: authorized_user file missing client_id, client_secret, or refresh_token; refusing to guess", path)
	}
	r.register.AddLiteral(f.ClientSecret)
	r.register.AddLiteral(f.RefreshToken)
	tokenURL := f.TokenURI // optional in authorized_user files; tests point it at httptest
	if tokenURL == "" {
		tokenURL = gcpDefaultTokenURL
	}
	tr, err := r.tokenRequest(ctx, tokenURL, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {f.ClientID},
		"client_secret": {f.ClientSecret},
		"refresh_token": {f.RefreshToken},
	})
	if err != nil {
		return nil, fmt.Errorf("gcp token refresh (%s): %w", tokenURL, err)
	}
	return r.credential(tr.AccessToken), nil
}

// parseRSAPrivateKey accepts the PKCS8 ("PRIVATE KEY") and PKCS1 ("RSA
// PRIVATE KEY") PEM blocks Google issues.
func parseRSAPrivateKey(pemData string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, errors.New("private_key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("private_key is neither PKCS1 nor PKCS8")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private_key is not RSA")
	}
	return key, nil
}

// signJWTRS256 builds the service-account assertion: RS256, one hour.
func signJWTRS256(key *rsa.PrivateKey, iss, scope, aud string, iat int64) (string, error) {
	enc := func(v any) (string, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	}
	header, err := enc(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := enc(map[string]any{
		"iss": iss, "scope": scope, "aud": aud, "iat": iat, "exp": iat + 3600,
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + claims
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
