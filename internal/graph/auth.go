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

// LoginHint is the command that fixes a missing token.
const LoginHint = "mail-find auth login --account outlook"

func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "mailkit")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mailkit")
}

// Settings are the app registration; from the environment or the config
// file `outlook.json` {"client_id":..., "tenant_id":...}.
type Settings struct {
	ClientID string `json:"client_id"`
	TenantID string `json:"tenant_id"`
}

func loadSettings() (Settings, error) {
	s := Settings{ClientID: os.Getenv("MAILKIT_OUTLOOK_CLIENT_ID"), TenantID: os.Getenv("MAILKIT_OUTLOOK_TENANT_ID")}
	if s.ClientID != "" && s.TenantID != "" {
		return s, nil
	}
	b, err := os.ReadFile(filepath.Join(configDir(), "outlook.json"))
	if err != nil {
		return s, fmt.Errorf("no Outlook app registration: write %s with client_id and tenant_id", filepath.Join(configDir(), "outlook.json"))
	}
	if err := unmarshal(b, &s); err != nil {
		return s, err
	}
	return s, nil
}

func credential(interactive bool) (azcore.TokenCredential, azidentity.AuthenticationRecord, error) {
	s, err := loadSettings()
	if err != nil {
		return nil, azidentity.AuthenticationRecord{}, err
	}
	c, err := cache.New(&cache.Options{Name: "mailkit"})
	if err != nil {
		return nil, azidentity.AuthenticationRecord{}, err
	}
	var rec azidentity.AuthenticationRecord
	recPath := filepath.Join(configDir(), "outlook-record.json")
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

// Client returns an HTTP client that adds a bearer token, or ErrAuth when
// no cached token exists.
func Client(ctx context.Context) (*http.Client, error) {
	cred, _, err := credential(false)
	if err != nil {
		return nil, mail.Wrap(providerName, "auth", LoginHint, err)
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

// Login runs the device-code flow and stores the authentication record so
// later runs are silent.
func Login(ctx context.Context, out func(string)) error {
	cred, _, err := credential(true)
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
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(configDir(), "outlook-record.json"), b, 0o600); err != nil {
		return err
	}
	out("Signed in as " + rec.Username)
	return nil
}
