package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Main runs mailmd. Ctrl-C and SIGTERM cancel the running command. It then
// stops cleanly, and the work it finished stays saved.
func Main(opts Options) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := New(opts)
	app.Root().SetContext(ctx)
	return app.RunIO(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
}
