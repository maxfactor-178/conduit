package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"conduit/config"
	"conduit/internal/auth"
	"conduit/internal/frontend"
)

// Server is the HTTP server for Conduit.
type Server struct {
	cfg        config.HTTPConfig
	xmppCfg    config.XMPPConfig
	authMapper *auth.Mapper
	wsHandler  http.Handler
	log        *slog.Logger
	httpServer *http.Server
}

// New creates a configured HTTP server. wsHandler handles /ws upgrade requests.
func New(
	cfg config.HTTPConfig,
	xmppCfg config.XMPPConfig,
	authMapper *auth.Mapper,
	wsHandler http.Handler,
	log *slog.Logger,
) *Server {
	return &Server{
		cfg:        cfg,
		xmppCfg:    xmppCfg,
		authMapper: authMapper,
		wsHandler:  wsHandler,
		log:        log,
	}
}

// Start builds the mux, starts listening, and blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Health endpoints (no auth).
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", s.readyzHandler)

	// Authenticated routes.
	authMiddleware := func(h http.Handler) http.Handler {
		return s.authMapper.Middleware(s.xmppCfg.Domain, h)
	}

	mux.Handle("/ws", authMiddleware(s.wsHandler))

	// Static frontend assets.
	mux.Handle("/", authMiddleware(frontend.Handler()))

	s.httpServer = &http.Server{
		Addr:         s.cfg.Addr,
		Handler:      securityHeaders(mux),
		ReadTimeout:  s.cfg.ReadTimeout,
		WriteTimeout: s.cfg.WriteTimeout,
	}

	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Addr, err)
	}
	s.log.Info("http server listening", "addr", s.cfg.Addr)

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		if err := s.httpServer.Shutdown(shutCtx); err != nil {
			s.log.Error("graceful shutdown error", "err", err)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

// securityHeaders wraps h with defensive HTTP response headers.
func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := w.Header()
		// Allow same-origin frames, scripts, styles, images and WebSocket
		// connections. Audio files are served from the same origin too.
		// style-src additionally allows the Bulma CDN stylesheet and the
		// inline style="" attributes used throughout index.html/app.js.
		hdr.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; "+
				"connect-src 'self' ws: wss:; media-src 'self'")
		hdr.Set("X-Frame-Options", "DENY")
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		hdr.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.ServeHTTP(w, r)
	})
}

// readyzHandler checks whether the XMPP server is reachable.
func (s *Server) readyzHandler(w http.ResponseWriter, r *http.Request) {
	addr := fmt.Sprintf("%s:%d", s.xmppCfg.Host, s.xmppCfg.Port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		http.Error(w, "xmpp unreachable", http.StatusServiceUnavailable)
		return
	}
	conn.Close()
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
