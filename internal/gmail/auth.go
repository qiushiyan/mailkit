package gmail

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/qiushiyan/mailkit/internal/mail"
)

// Scopes are the two this tool needs: read for mail-find, send for
// send-mail. Nothing wider; restricted scopes on an unverified app work
// only for the app's own test users, and a wide preset fails outright.
var Scopes = []string{
	"https://www.googleapis.com/auth/gmail.readonly",
	"https://www.googleapis.com/auth/gmail.send",
}

// Paths of the self-owned OAuth client and the token it yields. gcloud's
// application-default client is blocked from Gmail's restricted scopes, so
// a self-owned client is the only route.
func ConfigDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "mailkit")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mailkit")
}

func ClientSecretPath() string { return filepath.Join(ConfigDir(), "gmail-client.json") }
func TokenPath() string        { return filepath.Join(ConfigDir(), "gmail-token.json") }

// LoginHint is the command that fixes a missing or dead token.
const LoginHint = "mail-find auth login"

func loadConfig() (*oauth2.Config, error) {
	b, err := os.ReadFile(ClientSecretPath())
	if err != nil {
		return nil, fmt.Errorf("no OAuth client at %s: copy the Desktop-app client_secret.json there", ClientSecretPath())
	}
	return google.ConfigFromJSON(b, Scopes...)
}

// Client returns an authenticated HTTP client, or ErrAuth with the login
// command when no usable token exists.
func Client(ctx context.Context) (*http.Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, &mail.ProviderError{Provider: providerName, Op: "auth", Err: err}
	}
	b, err := os.ReadFile(TokenPath())
	if err != nil {
		return nil, &mail.ProviderError{Provider: providerName, Op: "auth", Err: mail.ErrAuth, Hint: LoginHint}
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, &mail.ProviderError{Provider: providerName, Op: "auth", Err: mail.ErrAuth, Hint: LoginHint}
	}
	src := cfg.TokenSource(ctx, &tok)
	return oauth2.NewClient(ctx, &savingSource{src: src, path: TokenPath()}), nil
}

// savingSource writes refreshed tokens back so a refresh survives the process.
type savingSource struct {
	src  oauth2.TokenSource
	path string
	last string
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	t, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	if t.AccessToken != s.last {
		s.last = t.AccessToken
		_ = saveToken(s.path, t)
	}
	return t, nil
}

func saveToken(path string, t *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// Login runs the browser OAuth flow on a loopback redirect and stores the
// token. It is the one interactive step.
func Login(ctx context.Context, out func(string)) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	cfg.RedirectURL = "http://" + ln.Addr().String() + "/"
	state := fmt.Sprintf("mk%d", os.Getpid())
	url := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)

	codeCh := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, "<p>mailkit: signed in. You can close this tab.</p>")
		codeCh <- r.URL.Query().Get("code")
	})}
	go srv.Serve(ln)
	defer srv.Close()

	out("Opening the browser to sign in. If it does not open, visit:\n    " + url)
	openBrowser(url)
	var code string
	select {
	case code = <-codeCh:
	case <-ctx.Done():
		return ctx.Err()
	}
	if code == "" {
		return errors.New("no authorisation code received")
	}
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return err
	}
	if err := saveToken(TokenPath(), tok); err != nil {
		return err
	}
	out("Token saved to " + TokenPath())
	return nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
