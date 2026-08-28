package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ataidesorg/friday/internal/core"
)

func azureResolver(t *testing.T, spy *spyRegistrar, env, getenv map[string]string) *Resolver {
	t.Helper()
	return NewResolver(spy, envOf(env),
		WithGetenv(func(k string) string { return getenv[k] }),
		WithKeyringLookup(func(context.Context, string, string) (string, bool, error) {
			return "", false, ErrKeyringUnavailable
		}),
	)
}

func TestAzureClientSecretGrant(t *testing.T) {
	var gotPath string
	var gotForm map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		gotForm = map[string]string{
			"grant_type": r.PostForm.Get("grant_type"), "client_id": r.PostForm.Get("client_id"),
			"client_secret": r.PostForm.Get("client_secret"), "scope": r.PostForm.Get("scope"),
		}
		fmt.Fprint(w, `{"access_token":"spy-azure-bearer-1","expires_in":3599}`)
	}))
	defer srv.Close()

	spy := &spyRegistrar{}
	r := azureResolver(t, spy, map[string]string{
		"AZURE_TENANT_ID":     "spy-tenant",
		"AZURE_CLIENT_ID":     "spy-client",
		"AZURE_CLIENT_SECRET": "spy-azure-secret-1", // gitleaks:allow
	}, map[string]string{"AZURE_AUTHORITY_HOST": srv.URL})
	cred, err := r.AzureBearer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cred.Zero()
	if got := string(cred.Value()); got != "spy-azure-bearer-1" {
		t.Fatalf("bearer = %q", got)
	}
	if gotPath != "/spy-tenant/oauth2/v2.0/token" {
		t.Errorf("token path = %q", gotPath)
	}
	want := map[string]string{
		"grant_type": "client_credentials", "client_id": "spy-client",
		"client_secret": "spy-azure-secret-1", "scope": azureScope,
	}
	for k, v := range want {
		if gotForm[k] != v {
			t.Errorf("form[%s] = %q, want %q", k, gotForm[k], v)
		}
	}
	for _, s := range []string{"spy-azure-secret-1", "spy-azure-bearer-1"} {
		if !spy.saw(s) {
			t.Errorf("%q not registered with redact", s)
		}
	}
}

func TestAzurePartialTripleFailsClosed(t *testing.T) {
	r := azureResolver(t, &spyRegistrar{}, map[string]string{"AZURE_TENANT_ID": "spy-tenant"}, nil)
	_, err := r.AzureBearer(context.Background())
	if err == nil || !strings.Contains(err.Error(), "partial triple fails closed") {
		t.Fatalf("err = %v", err)
	}
}

func TestAzureMissNamesTriple(t *testing.T) {
	r := azureResolver(t, &spyRegistrar{}, nil, nil)
	_, err := r.AzureBearer(context.Background())
	var miss *ErrNoCredential
	if !errors.As(err, &miss) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
	for _, want := range []string{"AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET", "friday auth set azure-foundry"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("miss lacks %q", want)
		}
	}
}

func TestAzureUnshippedCredentialsNotImplemented(t *testing.T) {
	for _, env := range []string{
		"AZURE_CLIENT_CERTIFICATE_PATH", "AZURE_FEDERATED_TOKEN_FILE", "MSI_ENDPOINT", "IDENTITY_ENDPOINT",
	} {
		r := azureResolver(t, &spyRegistrar{}, map[string]string{env: "set"}, nil)
		_, err := r.AzureBearer(context.Background())
		if !errors.Is(err, core.ErrNotImplemented) {
			t.Errorf("%s: err = %v, want NotImplemented", env, err)
		}
	}
}
