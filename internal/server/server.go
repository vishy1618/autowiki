package server

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/suvish/autowiki/internal/attachment"
	"github.com/suvish/autowiki/internal/auth"
	"github.com/suvish/autowiki/internal/chat"
	"github.com/suvish/autowiki/internal/chatsessions"
	"github.com/suvish/autowiki/internal/config"
	"github.com/suvish/autowiki/internal/dream"
	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)

type Server struct {
	cfg             *config.Config
	mux             *http.ServeMux
	dev             bool
	sessions        store.SessionStore
	chats           store.ChatStore
	streamer        chat.Streamer
	vault           *vault.Manager
	describer       attachment.Describer
	consolidate     dream.ConsolidateFn
	driveTokenStore store.DriveTokenStore // nil when drive sync is disabled
}

func New(cfg *config.Config, sessions store.SessionStore, chats store.ChatStore, streamer chat.Streamer, vm *vault.Manager, describer attachment.Describer, consolidate dream.ConsolidateFn, driveTokenStore store.DriveTokenStore, dev bool) *Server {
	s := &Server{
		cfg:             cfg,
		mux:             http.NewServeMux(),
		dev:             dev,
		sessions:        sessions,
		chats:           chats,
		streamer:        streamer,
		vault:           vm,
		describer:       describer,
		consolidate:     consolidate,
		driveTokenStore: driveTokenStore,
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	authCfg := auth.Config{
		GoogleClientID:     s.cfg.Auth.GoogleClientID,
		GoogleClientSecret: s.cfg.Auth.GoogleClientSecret,
		AllowedEmail:       s.cfg.Auth.AllowedEmail,
		SessionSecret:      s.cfg.Auth.SessionSecret,
		BaseURL:            s.cfg.BaseURL,
	}
	authHandler := auth.NewHandler(authCfg, s.sessions, s.driveTokenStore)
	mw := auth.NewMiddleware(s.sessions)

	// Public OAuth endpoints — no auth required.
	s.mux.HandleFunc("/api/auth/login", authHandler.Login)
	s.mux.HandleFunc("/api/auth/callback", authHandler.Callback)
	s.mux.HandleFunc("/api/auth/me", authHandler.Me)
	s.mux.HandleFunc("/api/auth/logout", authHandler.Logout)

	// Protected API routes.
	s.mux.Handle("/api/health", mw.Require(http.HandlerFunc(s.handleHealth)))
	s.mux.Handle("/api/chat", mw.Require(chat.NewHandler(s.chats, s.streamer, s.vault)))
	s.mux.Handle("/api/attachments", mw.Require(attachment.NewHandler(s.vault, s.describer)))
	s.mux.Handle("/api/chat-sessions", mw.Require(chatsessions.NewHandler(s.chats)))
	s.mux.Handle("/api/chat-sessions/", mw.Require(chatsessions.NewHandler(s.chats)))
	s.mux.Handle("/api/dream/run", mw.Require(dream.NewHandler(s.consolidate)))

	// All non-API routes serve the SPA unconditionally. Auth is enforced
	// client-side; the server only protects /api/* data endpoints.
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
