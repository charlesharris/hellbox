// Command slay is the hellbox terminal client.
//
// It is stateless and holds nothing the daemon does not. Killing it, or losing
// the SSH session it runs in, cannot disturb a rip in progress — which is the
// whole reason hellbox is split into a daemon and a client.
package main

import (
	"flag"
	"fmt"
	"os"

	"hellbox/internal/config"
	"hellbox/internal/tui"
	"hellbox/internal/version"
)

func main() {
	var (
		configPath  = flag.String("config", config.DefaultPath, "path to config.toml")
		socketPath  = flag.String("socket", "", "path to the hellboxd socket (overrides the config)")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("slay", version.Version)
		return
	}

	socket := *socketPath
	if socket == "" {
		// A missing or unreadable config is not fatal: the default socket path
		// is almost always right, and refusing to start a read-only client over
		// a config problem would be unhelpful.
		cfg, err := config.Load(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slay: %v\n", err)
			cfg = config.Default()
		}
		socket = cfg.SocketPath
	}

	if err := tui.Run(socket, version.Version); err != nil {
		fmt.Fprintf(os.Stderr, "slay: %v\n", err)
		os.Exit(1)
	}
}
