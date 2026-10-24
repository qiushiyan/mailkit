package graph

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity/cache"

	"github.com/qiushiyan/mailkit/internal/mail"
)

// Scopes are delegated: the app acts only as the signed-in user.
var Scopes = []string{"https://graph.microsoft.com/Mail.Read", "https://graph.microsoft.com/Mail.Send"}

// Settings are the app registration; from the environment or the file
// `outlook.json` {"client_id":..., "tenant_id":...} in the config directory.
type Settings struct {
	ClientID string `json:"client_id"`
	TenantID string `json:"tenant_id"`
}

func loadSettings(dir string) (Settings, error) {
	s := Settings{ClientID: os.Getenv("MAILKIT_OUTLOOK_CLIENT_ID"), TenantID: os.Getenv("MAILKIT_OUTLOOK_TENANT_ID")}
	if s.ClientID != "" && s.TenantID != "" {
		return s, nil
	}
	b, err := os.ReadFile(filepath.Join(dir, "outlook.json"))
	if err != nil {
		return s, fmt.Errorf("no Outlook app registration: write %s with client_id and tenant_id", filepath.Join(dir, "outlook.json"))
	}
	if err := unmarshal(b, &s); err != nil {
		return s, err
	}
	return s, nil
}

func credential(dir string, interactive bool) (azcore.TokenCredential, azidentity.AuthenticationRecord, error) {
	s, err := loadSettings(dir)
	if err != nil {
		return nil, azidentity.AuthenticationRecord{}, err
	}
	c, err := cache.New(&cache.Options{Name: "mailkit"})
	if err != nil {
		return nil, azidentity.AuthenticationRecord{}, err
	}
	var rec azidentity.AuthenticationRecord
	recPath := filepath.Join(dir, "outlook-record.json")
	if b, err := os.ReadFile(recPath); err == nil {
		_ = rec.UnmarshalJSON(b)
	}
	cred, err := azidentity.NewDeviceCodeCredential(&azidentity.DeviceCodeCredentialOptions{
		ClientID:                       s.ClientID,
		TenantID:                       s.TenantID,
		Cache:                          c,
		AuthenticationRecord:           rec,
		DisableAutomaticAuthentication: !interactive,
	})
	return cred, rec, err
}

// Client returns an HTTP client that adds a bearer token, from the
// registration and record in the config directory dir, or ErrAuth when no
// cached token exists.
func Client(ctx context.Context, dir string) (*http.Client, error) {
	cred, _, err := credential(dir, false)
	if err != nil {
		return nil, mail.Wrap(providerName, "auth", mail.LoginCommand(providerName), err)
	}
	return &http.Client{Transport: &bearer{cred: cred, next: http.DefaultTransport}}, nil
}

type bearer struct {
	cred azcore.TokenCredential
	next http.RoundTripper
}

func (b *bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	tok, err := b.cred.GetToken(r.Context(), policy.TokenRequestOptions{Scopes: Scopes})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", mail.ErrAuth, err)
	}
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+tok.Token)
	return b.next.RoundTrip(r2)
}

// Login runs the device-code flow and stores the authentication record in
// the config directory dir, so later runs are silent.
func Login(ctx context.Context, dir string, out func(string)) error {
	cred, _, err := credential(dir, true)
	if err != nil {
		return err
	}
	dc, ok := cred.(*azidentity.DeviceCodeCredential)
	if !ok {
		return fmt.Errorf("unexpected credential type")
	}
	rec, err := dc.Authenticate(ctx, &policy.TokenRequestOptions{Scopes: Scopes})
	if err != nil {
		return err
	}
	b, err := marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "outlook-record.json"), b, 0o600); err != nil {
		return err
	}
	out("Signed in as " + rec.Username)
	return nil
}
