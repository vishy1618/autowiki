package server

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/suvish/autowiki/internal/config"
)

type Server struct {
	cfg *config.Config
	mux *http.ServeMux
	dev bool
}

func New(cfg *config.Config, dev bool) *Server {
	s := &Server{cfg: cfg, mux: http.NewServeMux(), dev: dev}
	s.routes()
	return s
}

func (s *Server) routes() {
	// API routes
	s.mux.HandleFunc("/api/health", s.handleHealth)

	// SPA / static file handler — must be last
	if s.dev {
		s.mux.HandleFunc("/", s.proxyToRemixDev)
	} else {
		s.mux.HandleFunc("/", s.handleSPA)
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

// handleHealth is a basic liveness probe.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// handleSPA serves the built Remix SPA from the public/ directory.
// Any path that does not match a real file falls back to index.html.
func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	// Only serve non-API paths here; belt-and-suspenders guard.
	if strings.HasPrefix(r.URL.Path, "/api") {
		http.NotFound(w, r)
		return
	}

	publicDir := "public"
	path := filepath.Join(publicDir, filepath.Clean("/"+r.URL.Path))

	if _, err := os.Stat(path); os.IsNotExist(err) {
		http.ServeFile(w, r, filepath.Join(publicDir, "index.html"))
		return
	}

	http.FileServer(http.Dir(publicDir)).ServeHTTP(w, r)
}

// proxyToRemixDev forwards non-API traffic to the Remix dev server.
func (s *Server) proxyToRemixDev(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api") {
		http.NotFound(w, r)
		return
	}

	target, _ := url.Parse("http://localhost:5173")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ServeHTTP(w, r)
}
