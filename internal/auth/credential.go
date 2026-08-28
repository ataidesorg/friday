package auth

import "sync"

// Credential holds one secret in memory. The bytes never leave the process:
// they are registered with the redactor on creation and wiped by Zero. Value
// after Zero is empty.
type Credential struct {
	mu sync.Mutex
	b  []byte
}

func newCredential(b []byte) *Credential {
	return &Credential{b: b}
}

// Value returns the secret, or "" once Zero has run.
func (c *Credential) Value() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.b)
}

// Zero wipes the secret bytes. Safe to call more than once.
func (c *Credential) Zero() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.b {
		c.b[i] = 0
	}
	c.b = nil
}

// NewCredential wraps an already-obtained secret value, registering it with
// the redactor first so it can never print in clear. This is the seam for
// callers that receive a token from outside Resolve (tests, later OAuth
// flows); register must not be nil — a credential never exists unregistered.
func NewCredential(register Registrar, value string) *Credential {
	register.AddLiteral(value)
	return &Credential{b: []byte(value)}
}
