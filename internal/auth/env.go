package auth

import "github.com/ataidesorg/friday/internal/config"

// resolveEnv reads ref.Name from the credential environment.
func (r *Resolver) resolveEnv(ref config.AuthRef) (*Credential, error) {
	if v, ok := r.environ(ref.Name); ok && v != "" {
		return r.credential(v), nil
	}
	return nil, &ErrNoCredential{Source: "env", Where: ref.Name}
}
