// Command pcr queries and records production changes in PCR.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sarahmaeve/go-prod-change-registry/internal/pcrcli"
)

var (
	version   = "devel"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(pcrcli.Run(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr, pcrcli.BuildInfo{
		Version: version,
		Commit:  commit,
		Date:    buildDate,
	}))
}
