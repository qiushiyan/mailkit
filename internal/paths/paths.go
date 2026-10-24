// Package paths is where mailkit keeps what outlives one run: OAuth clients
// and tokens under Config, drafts and downloads under State. One value
// carries both, so every reader and writer of live state takes it from the
// caller, and a test points all of them at a temporary directory at once.
package paths

import (
	"os"
	"path/filepath"

	"github.com/qiushiyan/mailkit/internal/attachments"
)

// Dirs is the two roots of mailkit's files.
type Dirs struct {
	Config string
	State  string
}

// Default is $XDG_CONFIG_HOME/mailkit and $XDG_STATE_HOME/mailkit, or
// ~/.config/mailkit and ~/.local/state/mailkit when they are unset.
func Default() Dirs {
	home, _ := os.UserHomeDir()
	return Dirs{
		Config: under("XDG_CONFIG_HOME", filepath.Join(home, ".config")),
		State:  under("XDG_STATE_HOME", filepath.Join(home, ".local", "state")),
	}
}

func under(env, fallback string) string {
	if x := os.Getenv(env); x != "" {
		return filepath.Join(x, "mailkit")
	}
	return filepath.Join(fallback, "mailkit")
}

// Drafts is the draft store.
func (d Dirs) Drafts() string { return filepath.Join(d.State, "drafts") }

// Attachments is where a message's downloads land when the caller names no
// directory: under the state directory, never the working directory, so an
// agent cannot litter whatever directory it happens to be standing in.
func (d Dirs) Attachments(messageID string) string {
	return filepath.Join(d.State, "attachments", attachments.Safe(messageID, "message"))
}
