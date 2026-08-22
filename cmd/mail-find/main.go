// mail-find: read-only access to mail history, for an agent to consume.
//
// Read and send are separate binaries on purpose: wanting an agent to look
// something up in my mail should not also hand it the ability to send as me.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/qiushiyan/mailkit/internal/cli"
	"github.com/qiushiyan/mailkit/internal/wiring"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	root := cli.MailFind(wiring.Deps(), os.Stdout, os.Stderr)
	root.SetContext(ctx)
	os.Exit(cli.Run(root, os.Args[1:], os.Stderr))
}
