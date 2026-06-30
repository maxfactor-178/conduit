package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"conduit/config"
	"conduit/internal/auth"
	"conduit/internal/history"
	"conduit/internal/httpserver"
	"conduit/internal/session"
	"conduit/internal/user"
	internalxmpp "conduit/internal/xmpp"
	wshandler "conduit/internal/websocket"
)

const version = "0.1.0"

func main() {
	configPath := flag.String("config", "config/config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	if os.Getenv("DEV_MODE") == "true" {
		cfg.Dev.Enabled = true
	}
	if v := os.Getenv("DEV_USERNAME"); v != "" {
		cfg.Dev.Username = v
	}

	log := buildLogger(cfg.Log)

	if cfg.Dev.Enabled {
		log.Warn("DEV MODE ENABLED — do not use in production")
	}

	authMapper, err := auth.NewMapper(
		cfg.Auth.UsernameHeader,
		cfg.Auth.JIDMappingFile,
		cfg.Auth.CredentialsFile,
		cfg.Dev.Enabled,
		cfg.Dev.Username,
		cfg.Dev.Password,
	)
	if err != nil {
		log.Error("auth mapper init failed", "err", err)
		os.Exit(1)
	}

	xmppFactory := func(ctx context.Context, jid string) (internalxmpp.XMPPConn, error) {
		password, ok := authMapper.PasswordFor(jid)
		if !ok && cfg.Dev.Enabled {
			password = cfg.Dev.Password
		}
		if password == "" {
			return nil, fmt.Errorf("no password configured for %s", jid)
		}
		return internalxmpp.Dial(ctx, cfg.XMPP, jid, password, log)
	}

	sessionMgr := session.NewManager()
	userMgr := user.NewManager(xmppFactory, cfg.XMPP.IdleShutdown, log)
	histSvc := history.New(cfg.History.DefaultLimit, cfg.History.MaxLimit, log)

	wsHandler := wshandler.NewHandler(userMgr, sessionMgr, histSvc, cfg.HTTP.AllowedOrigins, cfg.Brand, log)
	httpSrv := httpserver.New(cfg.HTTP, cfg.XMPP, authMapper, wsHandler, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting conduit", "version", version)
	if err := httpSrv.Start(ctx); err != nil {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
	log.Info("shutdown complete")
}

func buildLogger(cfg config.LogConfig) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}
