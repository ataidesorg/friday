package auth

// Azure Entra fallback for azure-foundry: the client-secret grant
// only. Certificate, federated, and managed-identity credentials are real
// Azure mechanisms Ink has not implemented; when their env markers are
// present they fail as explicit NotImplemented, never silently skipped.
// The default scope is recorded from Azure docs and unverified against a
// live tenant.

import (
	"context"
	"fmt"
	"net/url"

	"github.com/ataidesorg/ink/internal/core"
)

const (
	azureScope         = "https://cognitiveservices.azure.com/.default"
	azureAuthorityHost = "https://login.microsoftonline.com"
)

// AzureBearer resolves an Entra access token via the client-credentials
// grant from AZURE_TENANT_ID / AZURE_CLIENT_ID / AZURE_CLIENT_SECRET.
func (r *Resolver) AzureBearer(ctx context.Context) (*Credential, error) {
	for env, feature := range map[string]string{
		"AZURE_CLIENT_CERTIFICATE_PATH": "azure certificate credential",
		"AZURE_FEDERATED_TOKEN_FILE":    "azure federated (workload identity) credential",
		"MSI_ENDPOINT":                  "azure managed-identity credential",
		"IDENTITY_ENDPOINT":             "azure managed-identity credential",
	} {
		if v, ok := r.environ(env); ok && v != "" {
			return nil, core.NotImplementedError{Feature: feature}
		}
	}
	tenant, _ := r.environ("AZURE_TENANT_ID")
	client, _ := r.environ("AZURE_CLIENT_ID")
	secret, _ := r.environ("AZURE_CLIENT_SECRET")
	if tenant == "" && client == "" && secret == "" {
		return nil, &ErrNoCredential{
			Source: "azure chain",
			Where:  "env AZURE_TENANT_ID + AZURE_CLIENT_ID + AZURE_CLIENT_SECRET",
			Hint:   "export the app-registration triple, or store a key with `ink auth set azure-foundry`",
		}
	}
	if tenant == "" || client == "" || secret == "" {
		return nil, fmt.Errorf("azure client-secret grant needs all of AZURE_TENANT_ID, AZURE_CLIENT_ID, AZURE_CLIENT_SECRET; a partial triple fails closed")
	}
	r.register.AddLiteral(secret)
	host := r.getenv("AZURE_AUTHORITY_HOST")
	if host == "" {
		host = azureAuthorityHost
	}
	tokenURL := host + "/" + url.PathEscape(tenant) + "/oauth2/v2.0/token"
	tr, err := r.tokenRequest(ctx, tokenURL, url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {client},
		"client_secret": {secret},
		"scope":         {azureScope},
	})
	if err != nil {
		return nil, fmt.Errorf("azure token endpoint (%s): %w", tokenURL, err)
	}
	return r.credential(tr.AccessToken), nil
}
