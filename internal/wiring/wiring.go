// Package wiring is the composition root: account name -> adapter. A third
// provider is one package plus one case here; nothing above it changes.
package wiring

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/qiushiyan/mailkit/internal/cli"
	"github.com/qiushiyan/mailkit/internal/drafts"
	"github.com/qiushiyan/mailkit/internal/gmail"
	"github.com/qiushiyan/mailkit/internal/graph"
	"github.com/qiushiyan/mailkit/internal/mail"
)

var accounts = []string{"gmail", "outlook"}

func open(ctx context.Context, account string) (mail.Mailbox, error) {
	switch account {
	case "gmail":
		client, err := gmail.Client(ctx)
		if err != nil {
			return nil, err
		}
		return gmail.New(ctx, client)
	case "outlook":
		client, err := graph.Client(ctx)
		if err != nil {
			return nil, err
		}
		return graph.New(client), nil
	}
	return nil, fmt.Errorf("unknown account %q; pick one of gmail, outlook", account)
}

func login(ctx context.Context, account string, out func(string)) error {
	switch account {
	case "gmail":
		return gmail.Login(ctx, out)
	case "outlook":
		return graph.Login(ctx, out)
	}
	return fmt.Errorf("unknown account %q", account)
}

// Deps wires the production adapters.
func Deps() cli.Deps {
	return cli.Deps{
		Open:        open,
		Login:       login,
		Drafts:      drafts.DefaultStore(),
		Accounts:    accounts,
		OpenPreview: func(path string) error { return exec.Command("open", path).Start() },
	}
}
