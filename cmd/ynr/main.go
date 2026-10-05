// Command ynr is your named reporting: the factory's observation plane.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/eyelock/ynr/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
