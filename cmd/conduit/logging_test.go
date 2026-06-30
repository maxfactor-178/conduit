package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"conduit/config"
)

func TestSyslogHandlerFormat(t *testing.T) {
	var buf bytes.Buffer
	h := newSyslogHandler(&buf, slog.LevelInfo)
	logger := slog.New(h)
	logger.Info("session_open", "jid", "alice@example.com", "remote", "10.0.0.5")

	line := buf.String()
	// local0 (16)*8 + info (6) = 134
	if !strings.HasPrefix(line, "<134>") {
		t.Errorf("expected <134> priority prefix, got: %q", line)
	}
	for _, want := range []string{"conduit[", "session_open", "jid=alice@example.com", "remote=10.0.0.5"} {
		if !strings.Contains(line, want) {
			t.Errorf("syslog line missing %q: %q", want, line)
		}
	}
	if !strings.HasSuffix(line, "\n") {
		t.Errorf("syslog line must end in newline: %q", line)
	}
}

func TestSyslogHandlerLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	h := newSyslogHandler(&buf, slog.LevelWarn)
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("info should be filtered when level is warn")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("error should pass when level is warn")
	}
}

func TestBuildLoggingSeparateAuditFile(t *testing.T) {
	dir := t.TempDir()
	appPath := filepath.Join(dir, "app.log")
	auditPath := filepath.Join(dir, "audit.log")

	log, auditLog, closers, err := buildLogging(config.LogConfig{
		Level:     "info",
		Format:    "json",
		File:      appPath,
		AuditFile: auditPath,
	})
	if err != nil {
		t.Fatalf("buildLogging: %v", err)
	}
	defer func() {
		for _, c := range closers {
			c.Close()
		}
	}()

	log.Info("app message")
	auditLog.SessionOpen("bob@example.com", "10.0.0.9", "sess-1")

	appData, _ := os.ReadFile(appPath)
	auditData, _ := os.ReadFile(auditPath)

	if !strings.Contains(string(appData), "app message") {
		t.Errorf("app log missing app message: %q", appData)
	}
	if strings.Contains(string(appData), "session_open") {
		t.Errorf("audit record leaked into app log: %q", appData)
	}
	if !strings.Contains(string(auditData), "session_open") || !strings.Contains(string(auditData), "bob@example.com") {
		t.Errorf("audit log missing session_open record: %q", auditData)
	}
}
