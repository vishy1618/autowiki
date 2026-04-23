package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/suvish/autowiki/internal/store"
)

const driveScope = "https://www.googleapis.com/auth/drive"

const (
	sessionCookieName   = "autowiki_session"
	sessionDuration     = 24 * time.Hour
	absoluteMaxLifetime = 30 * 24 * time.Hour
	stateCookieName     = "oauth_state"
)

// Config holds the auth-specific configuration.
type Config struct {
	GoogleClientID     string
	GoogleClientSecret string
	AllowedEmail       string
	SessionSecret      string
	BaseURL            string
}

// Handler implements the Google OAuth flow.
type Handler struct {
	cfg             Config
	oauthCfg        *oauth2.Config
	sessions        store.SessionStore
	driveTokenStore store.DriveTokenStore // nil when drive sync is disabled
	httpClient      *http.Client
}

// NewHandler constructs an auth Handler. Pass a non-nil driveTokenStore to
// enable Drive sync: the OAuth flow will request the drive.file scope with
// offline access and store the refresh token after a successful callback.
func NewHandler(cfg Config, sessions store.SessionStore, driveTokenStore store.DriveTokenStore) *Handler {
	scopes := []string{"openid", "email"}
	if driveTokenStore != nil {
		scopes = append(scopes, driveScope)
	}
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		RedirectURL:  cfg.BaseURL + "/api/auth/callback",
		Scopes:       scopes,
		Endpoint:     google.Endpoint,
	}
	return &Handler{
		cfg:             cfg,
		oauthCfg:        oauthCfg,
		sessions:        sessions,
		driveTokenStore: driveTokenStore,
		httpClient:      http.DefaultClient,
	}
}

// WithHTTPClient replaces the HTTP client used for token exchange and
// userinfo fetches. Useful for testing with a fake server.
func (h *Handler) WithHTTPClient(c *http.Client) {
	h.httpClient = c
}

// Logout deletes the session from the store and clears the session cookie.
// It is a no-op (still returns 200) if no valid cookie is present.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		h.sessions.DeleteSession(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	w.WriteHeader(http.StatusOK)
}

// Me returns the authenticated user's email as JSON, or 401 if not logged in.
// The frontend uses this as a lightweight auth probe on page load.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sess, err := h.sessions.GetSession(cookie.Value)
	if err != nil || sess == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"email":"` + sess.Email + `"}`))
}

// Login redirects the browser to Google's OAuth consent screen.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	state, err := randomToken(16)
	if err != nil {
		http.Error(w, "failed to generate state", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   300, // 5 minutes — long enough to complete the OAuth dance
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	accessType := oauth2.AccessTypeOnline
	if h.driveTokenStore != nil {
		accessType = oauth2.AccessTypeOffline
	}
	opts := []oauth2.AuthCodeOption{accessType}
	if h.driveTokenStore != nil {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", "consent"))
	}
	url := h.oauthCfg.AuthCodeURL(state, opts...)
	http.Redirect(w, r, url, http.StatusFound)
}

// Callback handles the redirect from Google after the user consents.
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	// Validate state to prevent CSRF.
	stateCookie, err := r.Cookie(stateCookieName)
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	// Exchange auth code for token using our HTTP client.
	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, h.httpClient)
	token, err := h.oauthCfg.Exchange(ctx, code)
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusBadRequest)
		return
	}

	// Fetch the user's email from Google's userinfo endpoint.
	email, err := h.fetchEmail(ctx, token)
	if err != nil {
		http.Error(w, "failed to fetch user info", http.StatusInternalServerError)
		return
	}

	if email != h.cfg.AllowedEmail {
		http.Redirect(w, r, "/login?error=unauthorized", http.StatusFound)
		return
	}

	// Issue session.
	sessionToken, err := randomToken(32)
	if err != nil {
		http.Error(w, "failed to generate session", http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC()
	sess := store.Session{
		Token:             sessionToken,
		Email:             email,
		CreatedAt:         now,
		ExpiresAt:         now.Add(sessionDuration),
		AbsoluteExpiresAt: now.Add(absoluteMaxLifetime),
	}
	if err := h.sessions.CreateSession(sess); err != nil {
		http.Error(w, "failed to store session", http.StatusInternalServerError)
		return
	}

	if h.driveTokenStore != nil && token.RefreshToken != "" {
		if err := h.driveTokenStore.SetDriveToken(token.RefreshToken); err != nil {
			http.Error(w, "failed to store drive token", http.StatusInternalServerError)
			return
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   int(sessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	// Clear the state cookie.
	http.SetCookie(w, &http.Cookie{
		Name:   stateCookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})

	http.Redirect(w, r, "/", http.StatusFound)
}

// fetchEmail retrieves the authenticated user's email from Google's userinfo endpoint.
func (h *Handler) fetchEmail(ctx context.Context, token *oauth2.Token) (string, error) {
	client := h.oauthCfg.Client(ctx, token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		return "", fmt.Errorf("userinfo request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading userinfo: %w", err)
	}

	// Parse just the email field without pulling in encoding/json everywhere.
	// Full struct decode happens in middleware where we have more context.
	type userInfo struct {
		Email string `json:"email"`
	}
	var ui userInfo
	if err := parseJSON(body, &ui); err != nil {
		return "", fmt.Errorf("parsing userinfo: %w", err)
	}
	return ui.Email, nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
