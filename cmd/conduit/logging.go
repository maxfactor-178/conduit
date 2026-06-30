package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"conduit/config"
	"conduit/internal/audit"
)

// buildLogging constructs the application logger and the audit logger from
// config, opening log files as needed. The returned closers must be closed on
// shutdown. The app log honours the configured format; the audit log uses the
// same format but is written to its own file when audit_file is set.
func buildLogging(cfg config.LogConfig) (*slog.Logger, *audit.Logger, []io.Closer, error) {
	var closers []io.Closer

	appOut, c, err := openOutput(cfg.File)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open log file %q: %w", cfg.File, err)
	}
	if c != nil {
		closers = append(closers, c)
	}

	level := parseLevel(cfg.Level)
	log := slog.New(newHandler(cfg.Format, appOut, level))

	// Audit log: dedicated file when configured, otherwise share the app output.
	auditOut := appOut
	if cfg.AuditFile != "" && cfg.AuditFile != cfg.File {
		out, ac, err := openOutput(cfg.AuditFile)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("open audit file %q: %w", cfg.AuditFile, err)
		}
		auditOut = out
		if ac != nil {
			closers = append(closers, ac)
		}
	}
	auditLog := slog.New(newHandler(cfg.Format, auditOut, slog.LevelInfo)).With("category", "audit")

	return log, audit.New(auditLog), closers, nil
}

// openOutput returns the writer for a log path. An empty path means stdout (no
// closer). A real path is opened append-only so external tools (logrotate with
// copytruncate) can rotate it underneath us.
func openOutput(path string) (io.Writer, io.Closer, error) {
	if path == "" {
		return os.Stdout, nil, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, nil, err
	}
	return f, f, nil
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func newHandler(format string, w io.Writer, level slog.Level) slog.Handler {
	opts := &slog.HandlerOptions{Level: level}
	switch format {
	case "json":
		return slog.NewJSONHandler(w, opts)
	case "syslog":
		return newSyslogHandler(w, level)
	default:
		return slog.NewTextHandler(w, opts)
	}
}

// syslogHandler writes records in a simple RFC 3164 (BSD syslog) line format:
//
//	<PRI>Mon _2 15:04:05 HOST conduit[PID]: msg key=value ...
//
// PRI encodes facility local0 plus the severity mapped from the slog level.
// This is the format most log collectors recognise out of the box.
type syslogHandler struct {
	w     io.Writer
	mu    *sync.Mutex
	level slog.Level
	host  string
	pid   int
	attrs string // pre-rendered " key=value" pairs from WithAttrs
}

func newSyslogHandler(w io.Writer, level slog.Level) *syslogHandler {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "-"
	}
	return &syslogHandler{w: w, mu: &sync.Mutex{}, level: level, host: host, pid: os.Getpid()}
}

const syslogFacilityLocal0 = 16

func (h *syslogHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *syslogHandler) Handle(_ context.Context, r slog.Record) error {
	pri := syslogFacilityLocal0*8 + syslogSeverity(r.Level)
	ts := r.Time.Format("Jan _2 15:04:05")

	var b strings.Builder
	fmt.Fprintf(&b, "<%d>%s %s conduit[%d]: %s", pri, ts, h.host, h.pid, r.Message)
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *syslogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range attrs {
		writeAttr(&b, a)
	}
	clone := *h
	clone.attrs = b.String()
	return &clone
}

// WithGroup is supported minimally: our codebase does not use attribute groups,
// so group names are ignored rather than namespaced.
func (h *syslogHandler) WithGroup(_ string) slog.Handler { return h }

func writeAttr(b *strings.Builder, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	b.WriteByte(' ')
	b.WriteString(a.Key)
	b.WriteByte('=')
	v := a.Value.String()
	if strings.ContainsAny(v, " \"") {
		fmt.Fprintf(b, "%q", v)
	} else {
		b.WriteString(v)
	}
}

func syslogSeverity(l slog.Level) int {
	switch {
	case l >= slog.LevelError:
		return 3 // err
	case l >= slog.LevelWarn:
		return 4 // warning
	case l >= slog.LevelInfo:
		return 6 // info
	default:
		return 7 // debug
	}
}
