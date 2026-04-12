package server

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/suvish/autowiki/internal/auth"
	"github.com/suvish/autowiki/internal/config"
	"github.com/suvish/autowiki/internal/store"
)

type Server struct {
	cfg      *config.Config
	mux      *http.ServeMux
	dev      bool
	sessions store.SessionStore
}

func New(cfg *config.Config, sessions store.SessionStore, dev bool) *Server {
	s := &Server{cfg: cfg, mux: http.NewServeMux(), dev: dev, sessions: sessions}
	s.routes()
	return s
}

func (s *Server) routes() {
	authCfg := auth.Config{
		GoogleClientID:     s.cfg.Auth.GoogleClientID,
		GoogleClientSecret: s.cfg.Auth.GoogleClientSecret,
		AllowedEmail:       s.cfg.Auth.AllowedEmail,
		SessionSecret:      s.cfg.Auth.SessionSecret,
		BaseURL:            fmt.Sprintf("http://localhost:%d", s.cfg.ServerPort),
	}
	authHandler := auth.NewHandler(authCfg, s.sessions)
	mw := auth.NewMiddleware(s.sessions)

	// Public OAuth endpoints — no auth required.
	s.mux.HandleFunc("/api/auth/login", authHandler.Login)
	s.mux.HandleFunc("/api/auth/callback", authHandler.Callback)

	// Protected API routes.
	s.mux.Handle("/api/health", mw.Require(http.HandlerFunc(s.handleHealth)))

	// SPA / static file handler — must be last.
	if s.dev {
		s.mux.Handle("/", mw.RequireOrRedirect(http.HandlerFunc(s.proxyToRemixDev)))
	} else {
		s.mux.Handle("/", mw.RequireOrRedirect(http.HandlerFunc(s.handleSPA)))
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.cfg.ServerPort)
	fmt.Printf("autowiki listening on %s\n", addr)
	return http.ListenAndServe(addr, s)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api") {
		http.NotFound(w, r)
		return
	}

	// /login must be reachable without auth — the middleware redirects here,
	// so the SPA itself is always served for this path.
	publicDir := "public"
	path := filepath.Join(publicDir, filepath.Clean("/"+r.URL.Path))

	if _, err := os.Stat(path); os.IsNotExist(err) {
		http.ServeFile(w, r, filepath.Join(publicDir, "index.html"))
		return
	}

	http.FileServer(http.Dir(publicDir)).ServeHTTP(w, r)
}

func (s *Server) proxyToRemixDev(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api") {
		http.NotFound(w, r)
		return
	}

	target, _ := url.Parse("http://localhost:5173")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ServeHTTP(w, r)
}
