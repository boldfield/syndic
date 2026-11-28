package auth

// Client identity (v9 delta, section 7): how the client names itself to an
// authorization server. In order: a client ID metadata document, dynamic
// registration, then mcp.<server>.client_id.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/boldfield/syndic/internal/secretstore"
)

// ClientMetadataURL is the client identifier by a client ID metadata
// document: the address where docs/oauth/client-metadata.json is served.
const ClientMetadataURL = "https://syndic.chat/oauth/client-metadata.json"

// RegistrationKind is the first part of the secret store key of a client
// registration.
const RegistrationKind = "mcp-client-registration"

// registrationRedirect is the loopback redirect a registration names; a
// loopback redirect may use any port (RFC 8252, section 7.3).
const registrationRedirect = "http://127.0.0.1/callback"

// maxRegistrationBytes bounds a registration response.
const maxRegistrationBytes = 1 << 20

// ClientIdentity is the client as one issuer knows it.
type ClientIdentity struct {
	ID string
	// Secret is the client secret a registration returned; empty otherwise.
	Secret string
}

// registration is the stored record of a dynamic registration.
type registration struct {
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// RegistrationKey is the secret store key of the registration made with one
// issuer.
func RegistrationKey(issuer string) []string {
	return []string{RegistrationKind, issuer}
}

// resolveClient picks the client identity for the issuer in disc. configured
// is mcp.<server>.client_id.
func (d *Discoverer) resolveClient(ctx context.Context, disc *Discovery, configured string, store *secretstore.Store) (ClientIdentity, error) {
	if disc.Metadata.ClientIDMetadataDocument {
		return ClientIdentity{ID: ClientMetadataURL}, nil
	}
	if disc.Metadata.RegistrationEndpoint != "" {
		return d.registeredClient(ctx, disc, store)
	}
	if configured != "" {
		return ClientIdentity{ID: configured}, nil
	}
	return ClientIdentity{}, errors.New("login: no client identity: the server accepts no metadata document and no registration; set mcp.<server>.client_id")
}

// registeredClient returns the registration stored under the issuer, making
// one first when there is none. A registration is never read under another
// issuer's key.
func (d *Discoverer) registeredClient(ctx context.Context, disc *Discovery, store *secretstore.Store) (ClientIdentity, error) {
	key := RegistrationKey(disc.Issuer)
	raw, _, err := store.Get(key)
	switch {
	case err == nil:
		var reg registration
		if json.Unmarshal(raw, &reg) == nil && reg.Issuer == disc.Issuer && reg.ClientID != "" {
			return ClientIdentity{ID: reg.ClientID, Secret: reg.ClientSecret}, nil
		}
	case !errors.Is(err, secretstore.ErrNotFound):
		return ClientIdentity{}, fmt.Errorf("login: read registration: %w", err)
	}
	reg, err := d.register(ctx, disc)
	if err != nil {
		return ClientIdentity{}, err
	}
	rec, err := json.Marshal(reg)
	if err != nil {
		return ClientIdentity{}, err
	}
	if _, err := store.Put(key, rec, false); err != nil {
		return ClientIdentity{}, fmt.Errorf("login: store registration: %w", err)
	}
	return ClientIdentity{ID: reg.ClientID, Secret: reg.ClientSecret}, nil
}

// register makes one dynamic registration (RFC 7591) naming the native
// application type.
func (d *Discoverer) register(ctx context.Context, disc *Discovery) (*registration, error) {
	body, err := json.Marshal(map[string]any{
		"client_name":                "syndic",
		"application_type":           "native",
		"redirect_uris":              []string{registrationRedirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, disc.Metadata.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client, done := d.clientFor(req.URL)
	defer done()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("login: registration request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistrationBytes+1))
	if err != nil {
		return nil, fmt.Errorf("login: registration response: %w", err)
	}
	if len(raw) > maxRegistrationBytes {
		return nil, errors.New("login: registration response is too large")
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("login: registration endpoint answered %d with no usable document", resp.StatusCode)
	}
	if (resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated) || out.Error != "" {
		return nil, fmt.Errorf("login: registration endpoint answered %d: %s", resp.StatusCode, oneLine(out.Error))
	}
	if out.ClientID == "" {
		return nil, errors.New("login: registration response carries no client_id")
	}
	return &registration{Issuer: disc.Issuer, ClientID: out.ClientID, ClientSecret: out.ClientSecret}, nil
}
