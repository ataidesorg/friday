package auth

// AWS credential chain: environment -> shared credentials file ->
// shared config file -> CLI cache -> IMDS (only when the owner set
// AWS_EC2_METADATA_SERVICE_ENDPOINT — Ink never probes link-local
// addresses uninvited). Credentials are memory-only, redact-registered the
// moment they resolve, and zeroed by the caller after each request.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AWSCredentials is the SigV4 signing triple. It is not a bearer: it signs
// requests instead of filling an Authorization header with itself.
type AWSCredentials struct {
	accessKeyID string
	secret      []byte
	session     []byte // empty for long-lived keys
}

// AccessKeyID identifies the key pair (public half of the credential).
func (c *AWSCredentials) AccessKeyID() string { return c.accessKeyID }

// SignRequest SigV4-signs req in place for region/service, hashing payload.
func (c *AWSCredentials) SignRequest(req *http.Request, payloadHash, region, service string, now time.Time) {
	signV4(req, payloadHash, c.accessKeyID, c.secret, c.session, region, service, now)
}

// Zero wipes the secret halves.
func (c *AWSCredentials) Zero() {
	zeroBytes(c.secret)
	zeroBytes(c.session)
	c.secret, c.session = nil, nil
}

func (r *Resolver) awsCredential(id, secret, session string) *AWSCredentials {
	r.register.AddLiteral(secret)
	if session != "" {
		r.register.AddLiteral(session)
	}
	r.register.AddLiteral(id)
	return &AWSCredentials{accessKeyID: id, secret: []byte(secret), session: []byte(session)}
}

// AWSCredentials walks the chain and returns the first hit. A malformed
// store fails closed; only a clean miss moves to the next source.
func (r *Resolver) AWSCredentials(ctx context.Context) (*AWSCredentials, error) {
	if id, ok := r.environ("AWS_ACCESS_KEY_ID"); ok && id != "" {
		secret, ok := r.environ("AWS_SECRET_ACCESS_KEY")
		if !ok || secret == "" {
			return nil, fmt.Errorf("AWS_ACCESS_KEY_ID is set but AWS_SECRET_ACCESS_KEY is not")
		}
		session, _ := r.environ("AWS_SESSION_TOKEN")
		return r.awsCredential(id, secret, session), nil
	}
	profile := r.getenv("AWS_PROFILE")
	if profile == "" {
		profile = "default"
	}
	credPath := r.getenv("AWS_SHARED_CREDENTIALS_FILE")
	if credPath == "" {
		credPath = filepath.Join(r.getenv("HOME"), ".aws", "credentials")
	}
	if cred, done, err := r.awsFromINI(credPath, profile, false); done {
		return cred, err
	}
	confPath := r.getenv("AWS_CONFIG_FILE")
	if confPath == "" {
		confPath = filepath.Join(r.getenv("HOME"), ".aws", "config")
	}
	if cred, done, err := r.awsFromINI(confPath, profile, true); done {
		return cred, err
	}
	cacheDir := filepath.Join(r.getenv("HOME"), ".aws", "cli", "cache")
	if cred, done, err := r.awsFromCLICache(cacheDir); done {
		return cred, err
	}
	if ep := r.getenv("AWS_EC2_METADATA_SERVICE_ENDPOINT"); ep != "" {
		return r.awsFromIMDS(ctx, ep)
	}
	return nil, &ErrNoCredential{
		Source: "aws chain",
		Where: fmt.Sprintf("env AWS_ACCESS_KEY_ID, %s [%s], %s, %s (IMDS off: AWS_EC2_METADATA_SERVICE_ENDPOINT unset)",
			credPath, profile, confPath, cacheDir),
		Hint: "export AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY or run `aws configure`",
	}
}

// awsFromINI reads one shared file. done=false means clean miss (absent
// file or profile); a present profile missing its secret fails closed.
func (r *Resolver) awsFromINI(path, profile string, configFile bool) (*AWSCredentials, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // well-known ~/.aws path or the user's own env override
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("read %s: %w", path, err)
	}
	sections := parseINI(string(data))
	name := profile
	if configFile && profile != "default" {
		name = "profile " + profile
	}
	section, ok := sections[name]
	if !ok {
		return nil, false, nil
	}
	id, secret := section["aws_access_key_id"], section["aws_secret_access_key"]
	if id == "" && secret == "" {
		return nil, false, nil // profile exists for other settings (region, SSO)
	}
	if id == "" || secret == "" {
		return nil, true, fmt.Errorf("%s profile %q holds a partial key pair; refusing to guess", path, profile)
	}
	return r.awsCredential(id, secret, section["aws_session_token"]), true, nil
}

// awsCachedCreds is the CLI cache / IMDS credential JSON shape.
type awsCachedCreds struct {
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Token           string `json:"Token"` // IMDS names the session token this
	Expiration      string `json:"Expiration"`
}

func (c awsCachedCreds) sessionToken() string {
	if c.SessionToken != "" {
		return c.SessionToken
	}
	return c.Token
}

// awsFromCLICache scans ~/.aws/cli/cache/*.json (written by `aws sso login`
// + CLI use, and by cached assume-role calls) for the unexpired credential
// with the latest expiry. Malformed JSON fails closed and names the file.
func (r *Resolver) awsFromCLICache(dir string) (*AWSCredentials, bool, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(paths) == 0 {
		return nil, false, nil
	}
	sort.Strings(paths)
	var best awsCachedCreds
	var bestExp time.Time
	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // globbed from the well-known ~/.aws/cli/cache dir
		if err != nil {
			continue
		}
		var doc struct {
			Credentials awsCachedCreds `json:"Credentials"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, true, fmt.Errorf("CLI cache %s: malformed JSON", path)
		}
		c := doc.Credentials
		if c.AccessKeyID == "" || c.SecretAccessKey == "" {
			continue
		}
		exp, err := time.Parse(time.RFC3339, c.Expiration)
		if err != nil || !exp.After(r.now().Add(2*time.Minute)) {
			continue // expired or unparseable expiry: a dead credential, skip
		}
		if exp.After(bestExp) {
			best, bestExp = c, exp
		}
	}
	if best.AccessKeyID == "" {
		return nil, false, nil
	}
	return r.awsCredential(best.AccessKeyID, best.SecretAccessKey, best.sessionToken()), true, nil
}

// awsFromIMDS fetches instance-role credentials from the endpoint the owner
// configured. IMDSv2 token first (best effort — v1 fallback keeps localhost
// emulators simple), then role name, then the credential document.
func (r *Resolver) awsFromIMDS(ctx context.Context, endpoint string) (*AWSCredentials, error) {
	base := strings.TrimRight(endpoint, "/")
	var imdsToken string
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/latest/api/token", nil)
	if err != nil {
		return nil, fmt.Errorf("IMDS endpoint %q: %w", endpoint, err)
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "21600")
	if resp, err := r.http.Do(req); err == nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			imdsToken = strings.TrimSpace(string(body))
		}
	}
	get := func(path string) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return "", err
		}
		if imdsToken != "" {
			req.Header.Set("X-aws-ec2-metadata-token", imdsToken)
		}
		resp, err := r.http.Do(req)
		if err != nil {
			return "", err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("IMDS %s: HTTP %d", path, resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return string(body), err
	}
	role, err := get("/latest/meta-data/iam/security-credentials/")
	if err != nil {
		return nil, err
	}
	role = strings.TrimSpace(strings.SplitN(role, "\n", 2)[0])
	if role == "" {
		return nil, fmt.Errorf("IMDS lists no instance role")
	}
	doc, err := get("/latest/meta-data/iam/security-credentials/" + role)
	if err != nil {
		return nil, err
	}
	var c awsCachedCreds
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		return nil, fmt.Errorf("IMDS role %s: malformed credential JSON", role)
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return nil, fmt.Errorf("IMDS role %s: credential document held no key pair", role)
	}
	return r.awsCredential(c.AccessKeyID, c.SecretAccessKey, c.sessionToken()), nil
}

// parseINI is the minimal shared-config reader: [section] headers,
// key = value lines, ; and # comments. Keys are lowercased.
func parseINI(data string) map[string]map[string]string {
	sections := map[string]map[string]string{}
	current := ""
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := sections[current]; !ok {
				sections[current] = map[string]string{}
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || current == "" {
			continue
		}
		sections[current][strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return sections
}
