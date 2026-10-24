// Package wiring is the composition root: account name -> adapter. A third
// provider is one package plus one case here; nothing above it changes.
package wiring

import (
	"context"
	"fmt"
	"strings"

	"github.com/qiushiyan/mailkit/internal/gmail"
	"github.com/qiushiyan/mailkit/internal/graph"
	"github.com/qiushiyan/mailkit/internal/mail"
	"github.com/qiushiyan/mailkit/internal/paths"
)

// Accounts are the names an account flag accepts, one per adapter.
var Accounts = []string{"gmail", "outlook"}

// Opener opens an account's adapter with the credentials under dirs.
func Opener(dirs paths.Dirs) mail.Opener {
	return func(ctx context.Context, account string) (mail.Mailbox, error) {
		switch account {
		case "gmail":
			client, err := gmail.Client(ctx, dirs.Config)
			if err != nil {
				return nil, err
			}
			return gmail.New(ctx, client)
		case "outlook":
			client, err := graph.Client(ctx, dirs.Config)
			if err != nil {
				return nil, err
			}
			return graph.New(client), nil
		}
		return nil, fmt.Errorf("unknown account %q; pick one of %s", account, strings.Join(Accounts, ", "))
	}
}

// Login runs an account's interactive sign-in, storing what it yields under
// dirs.
func Login(dirs paths.Dirs) func(ctx context.Context, account string, out func(string)) error {
	return func(ctx context.Context, account string, out func(string)) error {
		switch account {
		case "gmail":
			return gmail.Login(ctx, dirs.Config, out)
		case "outlook":
			return graph.Login(ctx, dirs.Config, out)
		}
		return fmt.Errorf("unknown account %q", account)
	}
}
