// send-mail: compose mail as an agent; send only what a human previewed.
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
	root := cli.SendMail(wiring.Deps(), os.Stdout, os.Stderr, os.Stdin)
	root.SetContext(ctx)
	os.Exit(cli.Run(root, os.Args[1:], os.Stderr))
}
