// Command hellboxd is the hellbox ripping daemon.
//
// It owns the optical drives, the state database, and the client socket, and it
// runs unattended: a disc that goes in is scanned, ripped, verified, and handed
// back with the tray open, with no interaction at any point.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"hellbox/internal/config"
	"hellbox/internal/daemon"
	"hellbox/internal/store"
)

func main() {
	var (
		configPath  = flag.String("config", config.DefaultPath, "path to config.toml")
		showVersion = flag.Bool("version", false, "print the version and exit")
		checkOnly   = flag.Bool("check", false, "run startup checks and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("hellboxd", daemon.Version)
		return
	}

	if err := run(*configPath, *checkOnly); err != nil {
		fmt.Fprintln(os.Stderr, "hellboxd:", err)
		os.Exit(1)
	}
}

func run(configPath string, checkOnly bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.StatePath)
	if err != nil {
		return err
	}
	defer st.Close()

	d := daemon.New(cfg, st)

	if checkOnly {
		return d.Check(context.Background(), os.Stdout)
	}

	// SIGTERM from systemd and SIGINT from a terminal both mean stop. Cancelling
	// the context propagates to any rip in flight, which makemkvcon is signalled
	// to end rather than being killed outright.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return d.Run(ctx)
}
