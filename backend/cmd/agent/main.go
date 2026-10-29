// Command agent is the WGPanel node agent - runs on each WireGuard server,
// registers with the control plane using a join token, then heartbeats over
// HTTPS+mTLS (docs/STORY-04-node-agent-mtls.md). Installed by deploy/install-node.sh.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	id, err := loadOrRegister(cfg, logger)
	if err != nil {
		return err
	}
	// Before "registered" is logged: the installers treat that line as progress, so a
	// mismatched interface must surface as "fatal" first.
	if err := checkNodeNetwork(cfg, id.Network, logger); err != nil {
		return err
	}
	logger.Info("registered", "node_id", id.NodeID, "reused", id.Reused)

	// Without a forwarder the node still works: heartbeats report it missing and the
	// panel gives this node's clients the public default resolvers instead.
	forwarder, err := startDNSForwarder(cfg.WGInterface, "/etc/resolv.conf", logger)
	if err != nil {
		logger.Warn("dns_forwarder_unavailable", "error", err)
	} else {
		defer forwarder.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return runHeartbeatLoop(ctx, cfg, id, forwarder != nil, logger)
}
