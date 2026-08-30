package container

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/redact"
)

var proxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "FTP_PROXY"}

func scrubEnv(in map[string]string, r *redact.Redactor) (map[string]string, error) {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k == "" || strings.Contains(k, "=") {
			return nil, fmt.Errorf("%w: malformed env key %q", core.ErrInvalidInput, k)
		}
		if slices.Contains(proxyVars, strings.ToUpper(k)) {
			continue
		}
		if r.ContainsSecret(v) || r.ContainsSecret(k+"="+v) {
			return nil, fmt.Errorf("%w: env %s looks like a secret", core.ErrSecretContent, k)
		}
		out[k] = v
	}
	return out, nil
}
