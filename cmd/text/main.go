package main

import (
	"context"
	"os"

	"github.com/KLIXPERT-io/text-cli/internal/cmd"
	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/update"
)

// version is injected at build time via -ldflags.
var version = "dev"

func main() {
	// Best-effort background self-update. A config that fails to load defaults
	// to opted-out, so main never fails here for a reason the user did not ask
	// about.
	optedOut := true
	if cfg, err := config.Load(); err == nil {
		optedOut = !config.AutoUpdateEnabled(cfg)
	}
	update.Background(context.Background(), version, optedOut)

	os.Exit(cmd.Execute(version))
}
