// Command gopherite is the single-binary entry point of the Gopherite
// Minecraft server. All resources are embedded: first launch only writes
// files the operator must edit (eula.txt, server.properties).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/masgzy/gopherite/config"
	"github.com/masgzy/gopherite/eula"
	"github.com/masgzy/gopherite/protocol/java/v776"
	"github.com/masgzy/gopherite/server"
)

// build info, overridable via -ldflags at release time.
var (
	version = "dev"
	commit  = "unknown"
)

const banner = `
   ____ __  __  ____  __ _   __ __ _  ___  ____
  / ___|  \/  |/ ___|/ _| | / _| | |/ _ \|  _ \
 | |  _| |\/| | |  _| |_| || |_| | | |_| | |_) |
 | |_| | |  | | |_| |  _|  \  _| | |  _| |  _ <
  \____|_|  |_|\____|_| |_(_)_|_|_|\____/|_| \_\
`

func main() {
	cfgPath := flag.String("config", config.Path, "configuration file path")
	dir := flag.String("dir", ".", "working directory for eula.txt and world data")
	acceptEULA := flag.Bool("accept-eula", false, "accept the Minecraft EULA and start")
	port := flag.Int("port", 0, "override server-port from the config")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	log.SetFlags(log.LstdFlags)
	fmt.Print(banner)
	if *showVersion {
		fmt.Printf("Gopherite %s (commit %s, protocol %d)\n", version, commit, v776.ProtocolNumber)
		return
	}

	// EULA gate, vanilla-compatible.
	if *acceptEULA {
		if err := eula.Accept(*dir); err != nil {
			log.Fatalf("eula: %v", err)
		}
	}
	st, err := eula.Ensure(*dir)
	if err != nil {
		log.Fatal(err)
	}
	_ = st

	cfg, generated, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	if generated {
		log.Printf("wrote default %s; edit it and restart to customise", *cfgPath)
	}
	if *port != 0 {
		cfg.ServerPort = *port
	}

	srv, err := server.New(server.Options{
		ListenAddr:         cfg.ListenAddr(),
		MOTD:               cfg.MOTD,
		MaxPlayers:         cfg.MaxPlayers,
		OnlineMode:         cfg.OnlineMode,
		VersionName:        v776.Name,
		ProtocolNumber:     v776.ProtocolNumber,
		FaviconPath:        cfg.IconPath,
		MaxPacketLen:       1 << 21,
		ReadTimeoutSeconds: cfg.ReadTimeout,
	})
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		log.Println("shutting down (world saving lands in M4)...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		os.Exit(0)
	}()

	log.Printf("Gopherite %s starting on %s (protocol %d, online-mode=%v)",
		version, cfg.ListenAddr(), v776.ProtocolNumber, cfg.OnlineMode)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
