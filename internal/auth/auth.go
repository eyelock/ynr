// Package auth is the central dashboard's sign-in (ADR-005, NFR-18): OpenID Connect's
// authorization-code flow with PKCE, in Go with coreos/go-oidc, after ynm ADR-017. ynm's ADR is
// about an API server that checks bearer tokens; a dashboard is a browser client, so this keeps
// that ADR's identity rules (any provider from its issuer URL, a person known by a handle made
// from issuer and subject, no email kept) and adds what a browser needs: the redirect flow, a
// signed session cookie, and logout.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/eyelock/ynr/internal/ui"
)

// Paths the sign-in answers on. The redirect URL registered with the provider must end in
// CallbackPath.
const (
	LoginPath     = "/auth/login"
	CallbackPath  = "/auth/callback"
	LogoutPath    = "/auth/logout"
	SignedOutPath = "/auth/signed-out"
)

const (
	sessionCookie = "ynr_session"
	loginCookie   = "ynr_login"
	loginTTL      = 10 * time.Minute
	// MinSecret is the shortest session secret accepted.
	MinSecret = 32
)

// Config is how sign-in is set up.
type Config struct {
	// Issuer is the provider's issuer URL; discovery supplies the rest.
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL is where the provider sends the browser back, ending in CallbackPath. It
	// must be https unless it names this machine's loopback.
	RedirectURL string
	// Who may sign in: any one rule suffices, and with none nobody may.
	Emails []string // exact addresses, compared without case
	Domain string   // an email domain, such as example.com
	// Group names a value the GroupClaim (default "groups") must contain.
	Group      string
	GroupClaim string
	// Secret signs the cookies; at least MinSecret bytes.
	Secret []byte
	// SessionTTL is how long a sign-in lasts, without renewal; eight hours when zero.
	SessionTTL time.Duration
	// HTTPClient, when set, is used to reach the provider.
	HTTPClient *http.Client
	Now        func() time.Time
}

// Auth guards a handler with sign-in.
type Auth struct {
	cfg      Config
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	redirect *url.URL
	secure   bool
	hc       *http.Client
}

// New discovers the provider and checks the settings. It fails rather than build a sign-in that
// would let everyone in or that cannot work.
func New(ctx context.Context, cfg Config) (*Auth, error) {
	switch {
	case cfg.Issuer == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURL == "":
		return nil, errors.New("sign-in needs an issuer, a client id, a client secret and a redirect URL")
	case len(cfg.Emails) == 0 && cfg.Domain == "" && cfg.Group == "":
		return nil, errors.New("sign-in needs to know who may sign in: an allowed email, domain or group")
	case len(cfg.Secret) < MinSecret:
		return nil, fmt.Errorf("the session secret must be at least %d bytes", MinSecret)
	}
	ru, err := url.Parse(cfg.RedirectURL)
	if err != nil || ru.Host == "" || (ru.Scheme != "https" && ru.Scheme != "http") {
		return nil, fmt.Errorf("redirect URL %q is not an http(s) URL", cfg.RedirectURL)
	}
	if ru.Path != CallbackPath {
		return nil, fmt.Errorf("redirect URL must end in %s", CallbackPath)
	}
	if ru.Scheme == "http" && !loopback(ru.Hostname()) {
		return nil, errors.New("redirect URL must be https unless it is on this machine's loopback")
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 8 * time.Hour
	}
	if cfg.GroupClaim == "" {
		cfg.GroupClaim = "groups"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	pctx := oidc.ClientContext(ctx, hc)
	p, err := oidc.NewProvider(pctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", cfg.Issuer, err)
	}
	return &Auth{
		cfg: cfg, redirect: ru, secure: ru.Scheme == "https", hc: hc,
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.ClientID, Now: cfg.Now}),
		oauth: oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: p.Endpoint(),
			RedirectURL: cfg.RedirectURL, Scopes: []string{oidc.ScopeOpenID, "email", "profile"}},
	}, nil
}

// AllowHost answers whether a request's Host is the one the redirect URL names, so a page
// elsewhere cannot reach the dashboard by pointing its own name at this server.
func (a *Auth) AllowHost(host string) bool {
	return hostKey(host, a.redirect.Scheme) == hostKey(a.redirect.Host, a.redirect.Scheme)
}

// hostKey lowercases a host and drops the scheme's default port.
func hostKey(host, scheme string) string {
	host = strings.ToLower(host)
	def := map[string]string{"https": "443", "http": "80"}[scheme]
	if h, p, err := net.SplitHostPort(host); err == nil && p == def {
		host = h
	}
	return strings.Trim(host, "[]")
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// Wrap puts sign-in in front of next: every path but the sign-in's own needs a session.
func (a *Auth) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case LoginPath:
			a.login(w, r)
		case CallbackPath:
			a.callback(w, r)
		case LogoutPath:
			a.logout(w, r)
		case SignedOutPath:
			a.signedOut(w, r)
		default:
			handle, ok := a.session(r)
			if !ok {
				a.unauthenticated(w, r)
				return
			}
			w.Header().Add("Vary", "Cookie")
			if !strings.HasPrefix(r.URL.Path, "/static/") {
				w.Header().Set("Cache-Control", "no-store")
			}
			next.ServeHTTP(w, r.WithContext(ui.WithUser(r.Context(), handle)))
		}
	})
}

// unauthenticated sends a browser navigation to sign in and refuses everything else (assets,
// the event stream, htmx fragments) with 401, which a script can see.
func (a *Auth) unauthenticated(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.Header.Get("HX-Request") == "" &&
		!strings.HasPrefix(r.URL.Path, "/static/") && r.URL.Path != "/tail/stream" {
		http.Redirect(w, r, LoginPath+"?"+url.Values{"next": {r.URL.RequestURI()}}.Encode(), http.StatusFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "sign in first", http.StatusUnauthorized)
}

// login starts the flow: a state, a nonce and a PKCE verifier go into a short-lived signed
// cookie, and the browser goes to the provider.
func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	l := loginState{State: random(), Nonce: random(), Verifier: oauth2.GenerateVerifier(),
		Next: localPath(r.URL.Query().Get("next")), Exp: a.cfg.Now().Add(loginTTL).Unix()}
	a.setCookie(w, loginCookie, a.seal("login", l), CallbackPath, loginTTL)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, a.oauth.AuthCodeURL(l.State, oauth2.S256ChallengeOption(l.Verifier),
		oidc.Nonce(l.Nonce)), http.StatusFound)
}

// callback finishes the flow. Any failure is a plain refusal that says little: the reason goes
// nowhere a caller could use it.
func (a *Auth) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var l loginState
	c, err := r.Cookie(loginCookie)
	if err != nil || !a.open("login", c.Value, &l) || a.cfg.Now().Unix() > l.Exp {
		http.Error(w, "sign-in expired; start again", http.StatusBadRequest)
		return
	}
	// The login cookie is single use.
	a.clearCookie(w, loginCookie, CallbackPath)
	q := r.URL.Query()
	if q.Get("error") != "" {
		http.Error(w, "the provider refused the sign-in", http.StatusForbidden)
		return
	}
	if q.Get("state") == "" || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(l.State)) != 1 {
		http.Error(w, "bad state", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(oidc.ClientContext(r.Context(), a.hc), 15*time.Second)
	defer cancel()
	tok, err := a.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(l.Verifier))
	if err != nil {
		http.Error(w, "could not complete sign-in", http.StatusBadGateway)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	id, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		http.Error(w, "could not verify the identity", http.StatusForbidden)
		return
	}
	if subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(l.Nonce)) != 1 {
		http.Error(w, "bad nonce", http.StatusBadRequest)
		return
	}
	var claims map[string]any
	if err := id.Claims(&claims); err != nil || !a.allowed(claims) {
		http.Error(w, "you are not allowed to sign in", http.StatusForbidden)
		return
	}
	exp := a.cfg.Now().Add(a.cfg.SessionTTL)
	s := session{Handle: Handle(id.Issuer, id.Subject), Exp: exp.Unix()}
	a.setCookie(w, sessionCookie, a.seal("session", s), "/", a.cfg.SessionTTL)
	http.Redirect(w, r, l.Next, http.StatusFound)
}

// allowed applies who may sign in. Email rules count only a verified email.
func (a *Auth) allowed(claims map[string]any) bool {
	email, _ := claims["email"].(string)
	verified, _ := claims["email_verified"].(bool)
	email = strings.ToLower(strings.TrimSpace(email))
	if email != "" && verified {
		for _, e := range a.cfg.Emails {
			if strings.EqualFold(strings.TrimSpace(e), email) {
				return true
			}
		}
		if d := strings.ToLower(strings.TrimPrefix(a.cfg.Domain, "@")); d != "" {
			if at := strings.LastIndex(email, "@"); at >= 0 && email[at+1:] == d {
				return true
			}
		}
	}
	if a.cfg.Group != "" {
		switch g := claims[a.cfg.GroupClaim].(type) {
		case []any:
			for _, v := range g {
				if s, ok := v.(string); ok && s == a.cfg.Group {
					return true
				}
			}
		case string:
			return g == a.cfg.Group
		}
	}
	return false
}

// logout ends the session. It is a POST so a page elsewhere cannot trigger it with a link.
func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.clearCookie(w, sessionCookie, "/")
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, SignedOutPath, http.StatusSeeOther)
}

func (a *Auth) signedOut(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprint(w, `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Signed out · ynr</title></head>`+
		`<body><p>You are signed out.</p><p><a href="`+LoginPath+`">Sign in</a></p></body></html>`)
}

// session reads the signed session cookie.
func (a *Auth) session(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	var s session
	if !a.open("session", c.Value, &s) || s.Handle == "" || a.cfg.Now().Unix() > s.Exp {
		return "", false
	}
	return s.Handle, true
}

// session is what the cookie holds: a handle and an end time, never an email or a name
// (NFR-16).
type session struct {
	Handle string `json:"h"`
	Exp    int64  `json:"e"`
}

type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"x"`
	Exp      int64  `json:"e"`
}

// Handle is how a person appears: "u" and 16 base32 characters of a hash of issuer and subject,
// as ynm ADR-017 names a person, so neither the subject nor an email is kept.
func Handle(issuer, subject string) string {
	sum := sha256.Sum256([]byte(issuer + "\x00" + subject))
	return "u" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:10]))
}

// seal signs a value for a purpose, so a login cookie is never accepted as a session.
func (a *Auth) seal(purpose string, v any) string {
	b, _ := json.Marshal(v)
	body := base64.RawURLEncoding.EncodeToString(b)
	return body + "." + base64.RawURLEncoding.EncodeToString(a.mac(purpose, body))
}

func (a *Auth) open(purpose, value string, v any) bool {
	body, sig, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, a.mac(purpose, body)) {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(body)
	return err == nil && json.Unmarshal(b, v) == nil
}

func (a *Auth) mac(purpose, body string) []byte {
	m := hmac.New(sha256.New, a.cfg.Secret)
	m.Write([]byte(purpose + "\x00" + body))
	return m.Sum(nil)
}

func (a *Auth) setCookie(w http.ResponseWriter, name, value, path string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
}

func (a *Auth) clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path, MaxAge: -1,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
}

// localPath keeps a return address on this site: a path, never another host.
func localPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") ||
		strings.HasPrefix(p, "/auth/") {
		return "/"
	}
	return p
}

func random() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// NewSecret makes a session secret for one run.
func NewSecret() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
