package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyelock/ynr/internal/query"
	"github.com/eyelock/ynr/internal/spool"
	"github.com/eyelock/ynr/internal/ui"
)

const (
	clientID     = "ynr-central"
	clientSecret = "s3cret-client"
)

// idp is a fake OpenID provider: discovery, keys, authorize and token, with keys made here.
type idp struct {
	*httptest.Server
	key *rsa.PrivateKey

	mu     sync.Mutex
	claims map[string]any // what the next sign-in's ID token says about the person
	nonce  string         // when set, the nonce the token carries instead of the request's
	codes  map[string]grant
}

type grant struct{ challenge, nonce, redirect string }

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{key: key, codes: map[string]grant{}, claims: map[string]any{"sub": "alice-1", "email": "alice@example.com", "email_verified": true}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": p.URL, "authorization_endpoint": p.URL + "/authorize",
			"token_endpoint": p.URL + "/token", "jwks_uri": p.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": b64(key.N.Bytes()), "e": "AQAB"}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("nonce") == "" ||
			q.Get("response_type") != "code" || q.Get("client_id") != clientID {
			http.Error(w, "bad authorize request", http.StatusBadRequest)
			return
		}
		code := b64([]byte(q.Get("state") + "-code"))
		p.mu.Lock()
		p.codes[code] = grant{q.Get("code_challenge"), q.Get("nonce"), q.Get("redirect_uri")}
		p.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, secret, _ := r.BasicAuth()
		if id == "" {
			id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		p.mu.Lock()
		g, ok := p.codes[r.Form.Get("code")]
		delete(p.codes, r.Form.Get("code"))
		claims, nonce := p.claims, p.nonce
		p.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if id != clientID || secret != clientSecret || !ok || r.Form.Get("redirect_uri") != g.redirect || b64(sum[:]) != g.challenge {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		if nonce == "" {
			nonce = g.nonce
		}
		c := map[string]any{"iss": p.URL, "aud": clientID, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce}
		for k, v := range claims {
			c[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": p.sign(t, c)})
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (p *idp) sign(t *testing.T, claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	c, _ := json.Marshal(claims)
	in := b64(h) + "." + b64(c)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + b64(sig)
}

func (p *idp) sets(claims map[string]any, nonce string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.claims, p.nonce = claims, nonce
}

// stub answers every query with no rows.
type stub struct{}

func (stub) Run(context.Context, *query.Query, query.Params) (*query.Result, error) {
	return &query.Result{Columns: []string{"x"}}, nil
}

// clock is a clock a test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// dash is the dashboard behind sign-in, a fake provider, and a browser with its own cookies.
type dash struct {
	t     *testing.T
	idp   *idp
	srv   *httptest.Server
	auth  *Auth
	web   *http.Client
	clock *clock
	spool string
}

func newDash(t *testing.T, mod func(*Config)) *dash {
	t.Helper()
	p := newIDP(t)
	root := t.TempDir()
	if err := spool.Init(root); err != nil {
		t.Fatal(err)
	}
	d := &dash{t: t, idp: p, clock: &clock{t: time.Now()}, spool: root}
	d.srv = httptest.NewUnstartedServer(nil)
	d.srv.Start()
	t.Cleanup(d.srv.Close)
	cfg := Config{Issuer: p.URL, ClientID: clientID, ClientSecret: clientSecret, RedirectURL: d.srv.URL + CallbackPath,
		Emails: []string{"Alice@Example.com"}, Secret: []byte(strings.Repeat("k", 40)), SessionTTL: time.Hour, Now: d.clock.now}
	if mod != nil {
		mod(&cfg)
	}
	a, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	d.auth = a
	d.srv.Config.Handler = ui.Handler(ui.Config{Runner: stub{}, Spool: root, AllowHost: a.AllowHost, Auth: a.Wrap})
	d.web = d.browser()
	return d
}

// browser follows no redirect by itself, so a test sees each hop.
func (d *dash) browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// visit follows redirects as a browser does and returns the last response and its body.
func (d *dash) visit(c *http.Client, method, u string) (*http.Response, string) {
	d.t.Helper()
	for range 10 {
		req, _ := http.NewRequest(method, u, nil)
		resp, err := c.Do(req)
		if err != nil {
			d.t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		loc := resp.Header.Get("Location")
		if loc == "" {
			return resp, string(b)
		}
		base, _ := url.Parse(u)
		next, _ := base.Parse(loc)
		u, method = next.String(), http.MethodGet
	}
	d.t.Fatal("too many redirects")
	return nil, ""
}

func (d *dash) signIn(c *http.Client) (*http.Response, string) {
	d.t.Helper()
	return d.visit(c, http.MethodGet, d.srv.URL+"/tail")
}

func (d *dash) cookie(c *http.Client, name string) *http.Cookie {
	u, _ := url.Parse(d.srv.URL)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

func TestUnauthenticatedRequestsAreRefused(t *testing.T) {
	d := newDash(t, nil)
	for _, path := range []string{"/", "/items", "/item?key=x", "/trace/abc", "/tail"} {
		req, _ := http.NewRequest(http.MethodGet, d.srv.URL+path, nil)
		r, err := d.web.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		if r.StatusCode != http.StatusFound || !strings.HasPrefix(r.Header.Get("Location"), LoginPath+"?next=") {
			t.Errorf("%s = %d %s, want a redirect to sign in", path, r.StatusCode, r.Header.Get("Location"))
		}
	}
	for _, tc := range []struct{ method, path, hx string }{
		{"GET", "/tail/stream", ""}, {"GET", "/tail/stream?service=ynh", ""}, {"GET", "/static/app.js", ""},
		{"GET", "/static/htmx.min.js", ""}, {"GET", "/items", "true"}, {"POST", "/", ""}, {"GET", "/nope", ""},
	} {
		req, _ := http.NewRequest(tc.method, d.srv.URL+tc.path, nil)
		if tc.hx != "" {
			req.Header.Set("HX-Request", tc.hx)
		}
		r, err := d.web.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		want := http.StatusUnauthorized
		if tc.hx == "" && tc.method == "GET" && !strings.HasPrefix(tc.path, "/tail/stream") && !strings.HasPrefix(tc.path, "/static") {
			want = http.StatusFound
		}
		if r.StatusCode != want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, r.StatusCode, want)
		}
	}
}

func TestSignInRoundTrip(t *testing.T) {
	d := newDash(t, nil)
	resp, body := d.signIn(d.web)
	if resp.StatusCode != 200 || !strings.Contains(body, "Live tail") {
		t.Fatalf("after sign-in: %d\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Sign out") || !strings.Contains(body, Handle(d.idp.URL, "alice-1")) {
		t.Errorf("the page shows no handle or sign-out:\n%s", body)
	}
	if strings.Contains(body, "alice@example.com") {
		t.Error("the page shows an email")
	}
	// The return address was kept, and the other pages and assets now answer.
	for _, path := range []string{"/", "/items", "/tail", "/static/app.js", "/static/htmx.min.js"} {
		if r, _ := d.visit(d.web, http.MethodGet, d.srv.URL+path); r.StatusCode != 200 {
			t.Errorf("%s = %d", path, r.StatusCode)
		}
	}
	// The login cookie is gone and the session cookie is what the design says.
	if d.cookie(d.web, loginCookie) != nil {
		t.Error("the login cookie outlived the callback")
	}
	req, _ := http.NewRequest(http.MethodGet, d.srv.URL+LoginPath, nil)
	r, _ := d.web.Do(req)
	_ = r.Body.Close()
	var login *http.Cookie
	for _, ck := range r.Cookies() {
		if ck.Name == loginCookie {
			login = ck
		}
	}
	if login == nil || !login.HttpOnly || login.SameSite != http.SameSiteLaxMode {
		t.Errorf("login cookie = %+v", login)
	}
	loc := r.Header.Get("Location")
	if !strings.Contains(loc, "code_challenge_method=S256") || !strings.Contains(loc, "state=") || !strings.Contains(loc, "nonce=") {
		t.Errorf("authorize URL lacks PKCE, state or nonce: %s", loc)
	}
}

func TestSessionCookieFlags(t *testing.T) {
	d := newDash(t, nil)
	// Follow by hand to see the callback's Set-Cookie.
	var setCookies []*http.Cookie
	u := d.srv.URL + "/"
	for range 10 {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		r, err := d.web.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		for _, ck := range r.Cookies() {
			if ck.Name == sessionCookie && ck.MaxAge > 0 {
				setCookies = append(setCookies, ck)
			}
		}
		if r.Header.Get("Location") == "" {
			break
		}
		base, _ := url.Parse(u)
		n, _ := base.Parse(r.Header.Get("Location"))
		u = n.String()
	}
	if len(setCookies) != 1 {
		t.Fatalf("session cookies set = %d", len(setCookies))
	}
	ck := setCookies[0]
	if !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode || ck.Path != "/" || ck.MaxAge != 3600 {
		t.Errorf("session cookie = %+v", ck)
	}
	if strings.Contains(ck.Value, "alice") {
		t.Error("the session cookie names the person")
	}

	// Behind https the cookies are Secure.
	d2 := newDash(t, func(c *Config) { c.RedirectURL = "https://ynr.example.com" + CallbackPath })
	req := httptest.NewRequest(http.MethodGet, LoginPath, nil)
	req.Host = "ynr.example.com"
	rec := httptest.NewRecorder()
	d2.srv.Config.Handler.ServeHTTP(rec, req)
	var login *http.Cookie
	for _, c := range rec.Result().Cookies() {
		login = c
	}
	if login == nil || !login.Secure || !login.HttpOnly || login.SameSite != http.SameSiteLaxMode {
		t.Errorf("https login cookie = %+v", login)
	}
}

func TestLiveTailNeedsASession(t *testing.T) {
	d := newDash(t, nil)
	if r, _ := d.visit(d.web, http.MethodGet, d.srv.URL+"/tail/stream"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned stream = %d", r.StatusCode)
	}
	d.signIn(d.web)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.srv.URL+"/tail/stream", nil)
	r, err := d.web.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("signed-in stream = %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
}

func TestWhoMaySignIn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mod    func(*Config)
		claims map[string]any
		want   int
	}{
		{"listed email, any case", nil, map[string]any{"sub": "a", "email": "ALICE@example.com", "email_verified": true}, 200},
		{"unlisted email", nil, map[string]any{"sub": "b", "email": "mallory@example.com", "email_verified": true}, 403},
		{"listed but unverified", nil, map[string]any{"sub": "a", "email": "alice@example.com", "email_verified": false}, 403},
		{"no email", nil, map[string]any{"sub": "a"}, 403},
		{"domain", func(c *Config) { c.Emails, c.Domain = nil, "corp.example" }, map[string]any{"sub": "c", "email": "bob@corp.example", "email_verified": true}, 200},
		{"lookalike domain", func(c *Config) { c.Emails, c.Domain = nil, "corp.example" }, map[string]any{"sub": "c", "email": "bob@evilcorp.example", "email_verified": true}, 403},
		{"subdomain", func(c *Config) { c.Emails, c.Domain = nil, "corp.example" }, map[string]any{"sub": "c", "email": "bob@x.corp.example", "email_verified": true}, 403},
		{"group", func(c *Config) { c.Emails, c.Group = nil, "ynr-viewers" }, map[string]any{"sub": "d", "groups": []string{"x", "ynr-viewers"}}, 200},
		{"wrong group", func(c *Config) { c.Emails, c.Group = nil, "ynr-viewers" }, map[string]any{"sub": "d", "groups": []string{"x"}}, 403},
		{"custom group claim", func(c *Config) { c.Emails, c.Group, c.GroupClaim = nil, "g", "roles" }, map[string]any{"sub": "d", "roles": []string{"g"}}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDash(t, tc.mod)
			d.idp.sets(tc.claims, "")
			resp, body := d.signIn(d.web)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.want, body)
			}
			signed := d.cookie(d.web, sessionCookie) != nil
			if signed != (tc.want == 200) {
				t.Errorf("session cookie present = %v", signed)
			}
			if tc.want == 403 {
				if r, _ := d.visit(d.web, http.MethodGet, d.srv.URL+"/static/app.js"); r.StatusCode != http.StatusUnauthorized {
					t.Errorf("a refused user reached an asset: %d", r.StatusCode)
				}
			}
		})
	}
}

func TestTamperedOrExpiredSessionIsRefused(t *testing.T) {
	d := newDash(t, nil)
	d.signIn(d.web)
	good := d.cookie(d.web, sessionCookie)
	u, _ := url.Parse(d.srv.URL)
	status := func(value string) int {
		c := d.browser()
		c.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: value, Path: "/"}})
		req, _ := http.NewRequest(http.MethodGet, d.srv.URL+"/items", nil)
		r, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		return r.StatusCode
	}
	if status(good.Value) != 200 {
		t.Fatal("a good session was refused")
	}
	body, sig, _ := strings.Cut(good.Value, ".")
	forged, _ := json.Marshal(session{Handle: "uforged", Exp: time.Now().Add(time.Hour).Unix()})
	for name, v := range map[string]string{
		"body changed":       b64(forged) + "." + sig,
		"signature flipped":  body + "." + strings.ToUpper(sig),
		"no signature":       body,
		"empty":              "",
		"garbage":            "a.b",
		"another key":        (&Auth{cfg: Config{Secret: []byte(strings.Repeat("z", 40))}}).seal("session", session{Handle: "u1", Exp: time.Now().Add(time.Hour).Unix()}),
		"a login as session": d.auth.seal("login", session{Handle: "u1", Exp: time.Now().Add(time.Hour).Unix()}),
	} {
		if got := status(v); got != http.StatusFound {
			t.Errorf("%s = %d, want a redirect to sign in", name, got)
		}
	}
	d.clock.add(2 * time.Hour)
	if got := status(good.Value); got != http.StatusFound {
		t.Errorf("an expired session = %d", got)
	}
}

func TestBadStateAndNonceAreRefused(t *testing.T) {
	// authorize walks to the provider and returns the callback URL it sends the browser to.
	authorize := func(d *dash) string {
		req, _ := http.NewRequest(http.MethodGet, d.srv.URL+LoginPath, nil)
		r, _ := d.web.Do(req)
		_ = r.Body.Close()
		req, _ = http.NewRequest(http.MethodGet, r.Header.Get("Location"), nil)
		r, _ = d.web.Do(req)
		_ = r.Body.Close()
		return r.Header.Get("Location")
	}
	call := func(d *dash, u string) int {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		r, err := d.web.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		return r.StatusCode
	}
	t.Run("state", func(t *testing.T) {
		d := newDash(t, nil)
		cb, _ := url.Parse(authorize(d))
		q := cb.Query()
		q.Set("state", "attacker-chosen")
		cb.RawQuery = q.Encode()
		if got := call(d, cb.String()); got != http.StatusBadRequest {
			t.Fatalf("bad state = %d", got)
		}
		if d.cookie(d.web, sessionCookie) != nil {
			t.Error("a session was set")
		}
	})
	t.Run("missing state", func(t *testing.T) {
		d := newDash(t, nil)
		cb, _ := url.Parse(authorize(d))
		q := cb.Query()
		q.Del("state")
		cb.RawQuery = q.Encode()
		if got := call(d, cb.String()); got != http.StatusBadRequest {
			t.Fatalf("missing state = %d", got)
		}
	})
	t.Run("nonce", func(t *testing.T) {
		d := newDash(t, nil)
		d.idp.sets(map[string]any{"sub": "a", "email": "alice@example.com", "email_verified": true}, "replayed-nonce")
		if resp, _ := d.signIn(d.web); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("bad nonce = %d", resp.StatusCode)
		}
		if d.cookie(d.web, sessionCookie) != nil {
			t.Error("a session was set")
		}
	})
	t.Run("no login cookie", func(t *testing.T) {
		d := newDash(t, nil)
		cb := authorize(d)
		other := d.browser() // a browser that never started the flow
		req, _ := http.NewRequest(http.MethodGet, cb, nil)
		r, _ := other.Do(req)
		_ = r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("callback without the login cookie = %d", r.StatusCode)
		}
	})
	t.Run("replay", func(t *testing.T) {
		d := newDash(t, nil)
		cb := authorize(d)
		if got := call(d, cb); got != http.StatusFound {
			t.Fatalf("first callback = %d", got)
		}
		if got := call(d, cb); got != http.StatusBadRequest {
			t.Fatalf("replayed callback = %d", got)
		}
	})
	t.Run("expired login", func(t *testing.T) {
		d := newDash(t, nil)
		cb := authorize(d)
		d.clock.add(time.Hour)
		if got := call(d, cb); got != http.StatusBadRequest {
			t.Fatalf("a callback an hour late = %d", got)
		}
	})
	t.Run("provider error", func(t *testing.T) {
		d := newDash(t, nil)
		authorize(d)
		if got := call(d, d.srv.URL+CallbackPath+"?error=access_denied"); got != http.StatusForbidden {
			t.Fatalf("provider error = %d", got)
		}
	})
}

func TestLogout(t *testing.T) {
	d := newDash(t, nil)
	d.signIn(d.web)
	if r, _ := d.visit(d.web, http.MethodGet, d.srv.URL+"/items"); r.StatusCode != 200 {
		t.Fatal("not signed in")
	}
	// A link from another page cannot sign anyone out.
	req, _ := http.NewRequest(http.MethodGet, d.srv.URL+LogoutPath, nil)
	if r, _ := d.web.Do(req); r.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET logout = %d", r.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, d.srv.URL+LogoutPath, nil)
	r, err := d.web.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != SignedOutPath {
		t.Fatalf("logout = %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	cleared := false
	for _, ck := range r.Cookies() {
		cleared = cleared || ck.Name == sessionCookie && ck.MaxAge < 0
	}
	if !cleared {
		t.Error("logout did not clear the session cookie")
	}
	if d.cookie(d.web, sessionCookie) != nil {
		t.Error("the browser kept the session cookie")
	}
	req, _ = http.NewRequest(http.MethodGet, d.srv.URL+"/items", nil)
	if r, _ := d.web.Do(req); r.StatusCode != http.StatusFound {
		t.Errorf("after logout /items = %d", r.StatusCode)
	}
	if r, body := d.visit(d.web, http.MethodGet, d.srv.URL+SignedOutPath); r.StatusCode != 200 || !strings.Contains(body, "signed out") {
		t.Errorf("signed-out page = %d %s", r.StatusCode, body)
	}
}

func TestOnlyTheRedirectHostIsAnswered(t *testing.T) {
	d := newDash(t, func(c *Config) { c.RedirectURL = "https://ynr.example.com" + CallbackPath })
	for host, want := range map[string]int{
		"ynr.example.com": http.StatusFound, "YNR.example.com": http.StatusFound, "ynr.example.com:443": http.StatusFound,
		"evil.example": http.StatusMisdirectedRequest, "127.0.0.1:4320": http.StatusMisdirectedRequest,
		"localhost": http.StatusMisdirectedRequest, "ynr.example.com:8443": http.StatusMisdirectedRequest,
		"ynr.example.com.evil.example": http.StatusMisdirectedRequest,
	} {
		for _, path := range []string{"/", LoginPath, "/static/app.js"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Host = host
			rec := httptest.NewRecorder()
			d.srv.Config.Handler.ServeHTTP(rec, req)
			w := want
			if path == "/static/app.js" && want == http.StatusFound {
				w = http.StatusUnauthorized
			}
			if rec.Code != w {
				t.Errorf("Host %s %s = %d, want %d", host, path, rec.Code, w)
			}
		}
	}
}

func TestReturnAddressStaysOnThisSite(t *testing.T) {
	d := newDash(t, nil)
	for next, want := range map[string]string{"/items": "/items", "//evil.example": "/", "https://evil.example/": "/", "/\\evil.example": "/", "/auth/login": "/"} {
		c := d.browser()
		req, _ := http.NewRequest(http.MethodGet, d.srv.URL+LoginPath+"?"+url.Values{"next": {next}}.Encode(), nil)
		r, _ := c.Do(req)
		_ = r.Body.Close()
		cur := r.Header.Get("Location")
		for range 5 {
			req, _ = http.NewRequest(http.MethodGet, cur, nil)
			r, _ = c.Do(req)
			_ = r.Body.Close()
			if cur = r.Header.Get("Location"); !strings.HasPrefix(cur, "http") {
				break
			}
		}
		if cur != want {
			t.Errorf("next %q returned to %q, want %q", next, cur, want)
		}
	}
}

func TestSetupIsRefusedRatherThanOpen(t *testing.T) {
	p := newIDP(t)
	base := Config{Issuer: p.URL, ClientID: clientID, ClientSecret: clientSecret, RedirectURL: "https://ynr.example.com/auth/callback",
		Emails: []string{"a@example.com"}, Secret: []byte(strings.Repeat("k", 40))}
	for name, mod := range map[string]func(*Config){
		"no issuer":           func(c *Config) { c.Issuer = "" },
		"no client id":        func(c *Config) { c.ClientID = "" },
		"no client secret":    func(c *Config) { c.ClientSecret = "" },
		"no redirect":         func(c *Config) { c.RedirectURL = "" },
		"nobody allowed":      func(c *Config) { c.Emails = nil },
		"short secret":        func(c *Config) { c.Secret = []byte("short") },
		"no secret":           func(c *Config) { c.Secret = nil },
		"plain http redirect": func(c *Config) { c.RedirectURL = "http://ynr.example.com/auth/callback" },
		"wrong redirect path": func(c *Config) { c.RedirectURL = "https://ynr.example.com/cb" },
		"unreachable issuer":  func(c *Config) { c.Issuer = "http://127.0.0.1:1" },
	} {
		cfg := base
		mod(&cfg)
		if _, err := New(context.Background(), cfg); err == nil {
			t.Errorf("%s: sign-in was built", name)
		}
	}
	if _, err := New(context.Background(), base); err != nil {
		t.Fatalf("a good setup was refused: %v", err)
	}
	loop := base
	loop.RedirectURL = "http://localhost:4320/auth/callback"
	if _, err := New(context.Background(), loop); err != nil {
		t.Fatalf("a loopback http redirect was refused: %v", err)
	}
}

func TestHandleKeepsNoSubject(t *testing.T) {
	h := Handle("https://idp.example", "alice")
	if h == Handle("https://idp.example", "bob") || h == Handle("https://other.example", "alice") || strings.Contains(h, "alice") || len(h) != 17 {
		t.Errorf("handle = %s", h)
	}
}
