package rest

// OAuth 2.0 + PKCE wrapper around SAGE's bearer-token MCP transport.
//
// Why this exists (v6.7.2):
//   ChatGPT's MCP connector form requires Authorization URL + Token URL —
//   the form's red banner explicitly rejects "static bearer in Client ID"
//   as an auth method. SAGE's HTTP MCP transport (v6.7.0/v6.7.1) only
//   accepts a long-lived bearer in the Authorization header. To close the
//   gap without disturbing v6.7.1, this layer presents a standards-track
//   OAuth flow whose `access_token` IS the existing bearer.
//
// Endpoints (mounted at the host root, not under /v1/mcp):
//   GET  /.well-known/oauth-authorization-server  RFC 8414 discovery doc
//   GET  /oauth/authorize                          consent screen + code mint
//   POST /oauth/authorize                          consent submission
//   POST /oauth/token                              code → bearer redemption
//
// Discovery URL placement: ChatGPT auto-discovers from the MCP server URL's
// host. The discovery doc MUST live at the root (`/.well-known/...`), not
// under `/v1/mcp/...` — the host is the OAuth issuer.
//
// Dynamic Client Registration (RFC 7591): NOT implemented in v6.7.2. The
// ChatGPT form's "User-Defined OAuth Client" path doesn't need DCR; the
// metadata simply omits `registration_endpoint`. Future work.
//
// Bearer reuse: at /authorize approval time we mint a fresh bearer via the
// existing IssueMCPToken path (so the operator gets a normal mcp_tokens row
// they can revoke from the dashboard), and stash the plaintext into the
// auth-code row. /token redeems it back out and returns it as access_token.
// Sessions to /v1/mcp/sse continue to use the same Authorization: Bearer
// scheme as before. Zero changes to the bearer-auth middleware.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/l33tdawg/sage/internal/store"
)

// AuthCodeTTL is the lifetime of an issued OAuth authorization code. RFC
// 6749 §4.1.2 recommends ≤10 minutes; SAGE uses 5.
const AuthCodeTTL = 5 * time.Minute

// OAuthStore is the storage surface OAuthHandler needs. The full
// SQLiteStore satisfies it.
type OAuthStore interface {
	// Token table — to mint a bearer at /authorize approval and to list the
	// operator's existing tokens for the consent screen dropdown.
	InsertMCPToken(ctx context.Context, id, name, agentID, tokenSHA256 string) error
	ListMCPTokens(ctx context.Context) ([]*store.MCPToken, error)
	// Auth-code table.
	IssueAuthCode(ctx context.Context,
		code, tokenID, codeChallenge, codeChallengeMethod, redirectURI, clientID, state, bearerPlaintext string,
		ttl time.Duration) error
	RedeemAuthCode(ctx context.Context, code, codeVerifier, redirectURI string) (string, error)
	// Agent listing — for the consent screen's pre-filled dropdown so the
	// operator doesn't have to copy/paste a 64-char hex pubkey.
	ListAgents(ctx context.Context) ([]*store.AgentEntry, error)
}

// OAuthSessionChecker reports whether the inbound request is authenticated
// for the SAGE dashboard. Used by /oauth/authorize to gate the consent
// screen. If `false`, the second return is the location header to redirect
// the user to (typically `/ui/?next=<encoded /oauth/authorize URL>`).
//
// On nodes without encryption enabled, the dashboard considers all requests
// authenticated, so this returns (true, "") and /authorize serves directly.
type OAuthSessionChecker func(r *http.Request) (authenticated bool, loginRedirect string)

// OAuthHandler bundles the OAuth 2.0 endpoints. Built once at server start,
// mounted on the chi router at the host root.
type OAuthHandler struct {
	Store         OAuthStore
	IsAuthed      OAuthSessionChecker
	IssuerBaseURL func(r *http.Request) string // e.g. "https://host:8443" — derived per request
}

// NewOAuthHandler constructs a handler with sensible defaults.
//
// `issuer` may be nil; if so the handler infers the issuer from the inbound
// request (`scheme://host`).
func NewOAuthHandler(s OAuthStore, isAuthed OAuthSessionChecker, issuer func(r *http.Request) string) *OAuthHandler {
	if isAuthed == nil {
		// Default: open. (Test convenience — production mounts the dashboard checker.)
		isAuthed = func(_ *http.Request) (bool, string) { return true, "" }
	}
	if issuer == nil {
		issuer = inferIssuer
	}
	return &OAuthHandler{Store: s, IsAuthed: isAuthed, IssuerBaseURL: issuer}
}

// inferIssuer reconstructs `scheme://host` from the request. Honors
// X-Forwarded-Proto / X-Forwarded-Host for reverse-proxy setups.
func inferIssuer(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xfp := r.Header.Get("X-Forwarded-Proto"); xfp != "" {
		scheme = xfp
	}
	host := r.Host
	if xfh := r.Header.Get("X-Forwarded-Host"); xfh != "" {
		host = xfh
	}
	return scheme + "://" + host
}

// HandleDiscovery serves the RFC 8414 OAuth Authorization Server Metadata
// document. ChatGPT's MCP connector reads this to auto-populate
// authorization_endpoint and token_endpoint.
func (h *OAuthHandler) HandleDiscovery(w http.ResponseWriter, r *http.Request) {
	issuer := h.IssuerBaseURL(r)
	doc := map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/oauth/authorize",
		"token_endpoint":                        issuer + "/oauth/token",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mcp"},
		"registration_endpoint":                 issuer + "/oauth/register",
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_ = json.NewEncoder(w).Encode(doc)
}

// HandleClientRegister implements RFC 7591 Dynamic Client Registration.
// ChatGPT's MCP connector REQUIRES this endpoint — without it, the connector
// silently fails before making any user-visible request, hanging on
// "Continue to SAGE" indefinitely. We accept the registration metadata
// verbatim, mint a fresh random client_id, and echo it back. Since we use
// PKCE on the authorization-code flow, we don't need to authenticate the
// client at the token endpoint, so client_secret isn't issued (clients are
// "public" per RFC 7591 §2). Registration is open — anyone can register a
// client. The actual auth gate is the user's consent decision at /authorize
// + their valid bearer token at the token endpoint via PKCE verification.
func (h *OAuthHandler) HandleClientRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		RedirectURIs            []string `json:"redirect_uris,omitempty"`
		ClientName              string   `json:"client_name,omitempty"`
		GrantTypes              []string `json:"grant_types,omitempty"`
		ResponseTypes           []string `json:"response_types,omitempty"`
		Scope                   string   `json:"scope,omitempty"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
		ApplicationType         string   `json:"application_type,omitempty"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	// Generate a public-client identifier. 16 random bytes base64url-encoded
	// (~22 chars) is well over the OAuth-spec minimum and unguessable.
	rawID := make([]byte, 16)
	if _, err := rand.Read(rawID); err != nil {
		http.Error(w, "client_id generation failed", http.StatusInternalServerError)
		return
	}
	clientID := base64.RawURLEncoding.EncodeToString(rawID)

	// Default to authorization_code + S256 if the client didn't specify.
	if len(req.GrantTypes) == 0 {
		req.GrantTypes = []string{"authorization_code"}
	}
	if len(req.ResponseTypes) == 0 {
		req.ResponseTypes = []string{"code"}
	}
	if req.TokenEndpointAuthMethod == "" {
		req.TokenEndpointAuthMethod = "none"
	}

	resp := map[string]any{
		"client_id":                  clientID,
		"client_id_issued_at":        time.Now().Unix(),
		"redirect_uris":              req.RedirectURIs,
		"grant_types":                req.GrantTypes,
		"response_types":             req.ResponseTypes,
		"token_endpoint_auth_method": req.TokenEndpointAuthMethod,
		"client_name":                req.ClientName,
		"scope":                      req.Scope,
		// No client_secret — PKCE is the proof-of-possession mechanism.
		// No expiration — public clients in RFC 7591 §2 may be perpetual.
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleProtectedResource serves the RFC 9728 Protected Resource Metadata
// document. The MCP authorization spec (June 2025 revision) requires MCP
// servers to expose this so clients can discover the authorization server
// associated with the MCP resource. ChatGPT's MCP connector follows the
// WWW-Authenticate header from a 401 to this URL, parses it, then bootstraps
// the OAuth flow against the authorization_servers entry — without it the
// connector hangs at "Continue" with no way to learn how to auth.
func (h *OAuthHandler) HandleProtectedResource(w http.ResponseWriter, r *http.Request) {
	issuer := h.IssuerBaseURL(r)
	doc := map[string]any{
		// The resource being protected is the MCP transport endpoint. Per
		// RFC 9728 §3, this is the canonical URL clients use to identify
		// the resource server.
		"resource":              issuer + "/v1/mcp/sse",
		"authorization_servers": []string{issuer},
		// Clients use bearer tokens issued via the authorization-code flow
		// against /oauth/token. No DPoP for v6.7.x.
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"mcp"},
		// Resource metadata is otherwise free-form; we keep it minimal so the
		// surface area we maintain matches what we actually implement.
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_ = json.NewEncoder(w).Encode(doc)
}

// authorizeFormParams captures the validated /authorize query parameters.
type authorizeFormParams struct {
	ClientID            string
	RedirectURI         string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	ResponseType        string
	Scope               string
}

// parseAuthorizeParams reads + validates the /authorize parameters from
// either the URL query (GET render of the consent screen) or the form body
// (POST submission of the consent form). r.FormValue checks form first then
// URL — perfect since the consent form's hidden inputs carry the original
// query params verbatim, but the GET path only has them in the query.
//
// Returns a problem-detail string for the caller to surface as a 400 if
// invalid.
func parseAuthorizeParams(r *http.Request) (authorizeFormParams, string) {
	// Parse form body for POST; FormValue auto-handles GET via r.URL.Query.
	_ = r.ParseForm()
	p := authorizeFormParams{
		ClientID:            strings.TrimSpace(r.FormValue("client_id")),
		RedirectURI:         strings.TrimSpace(r.FormValue("redirect_uri")),
		State:               r.FormValue("state"),
		CodeChallenge:       strings.TrimSpace(r.FormValue("code_challenge")),
		CodeChallengeMethod: strings.ToUpper(strings.TrimSpace(r.FormValue("code_challenge_method"))),
		ResponseType:        strings.ToLower(strings.TrimSpace(r.FormValue("response_type"))),
		Scope:               strings.TrimSpace(r.FormValue("scope")),
	}
	if p.ClientID == "" {
		return p, "client_id is required"
	}
	if p.RedirectURI == "" {
		return p, "redirect_uri is required"
	}
	if _, err := url.Parse(p.RedirectURI); err != nil {
		return p, "redirect_uri must be a valid URL"
	}
	if p.ResponseType == "" {
		p.ResponseType = "code"
	}
	if p.ResponseType != "code" {
		return p, "response_type must be 'code'"
	}
	if p.CodeChallenge == "" {
		return p, "code_challenge is required (PKCE)"
	}
	if p.CodeChallengeMethod == "" {
		p.CodeChallengeMethod = "S256"
	}
	if p.CodeChallengeMethod != "S256" {
		return p, "code_challenge_method must be S256"
	}
	return p, ""
}

// HandleAuthorize serves the consent screen (GET) and processes consent
// submission (POST). The user must be authenticated to the dashboard
// (when encryption is on) — IsAuthed gates that.
func (h *OAuthHandler) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	params, problem := parseAuthorizeParams(r)
	if problem != "" {
		http.Error(w, problem, http.StatusBadRequest)
		return
	}

	// Dashboard auth gate. If unauthenticated, redirect to the SPA with a
	// `next` param so the SPA login flow round-trips back here on success.
	if h.IsAuthed != nil {
		ok, loginURL := h.IsAuthed(r)
		if !ok {
			if loginURL == "" {
				// Build "/ui/?next=<full /oauth/authorize URL with query>"
				next := r.URL.RequestURI()
				loginURL = "/ui/?next=" + url.QueryEscape(next)
			}
			http.Redirect(w, r, loginURL, http.StatusFound)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		h.renderConsent(w, r, params, "")
	case http.MethodPost:
		h.processConsent(w, r, params)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// consentTemplate is the minimal HTML form. Lists existing active tokens as
// radio choices; offers a "mint a new token" path that takes a name + agent.
//
// We keep the markup deliberately spartan — this page is operator-facing,
// rarely seen, and ChatGPT's redirect destination so it doesn't need to be
// branded.
var consentTemplate = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Authorize MCP client — SAGE</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; max-width: 640px; margin: 4em auto; padding: 0 1em; color: #222; }
  h1 { font-size: 1.4em; margin-bottom: 0.2em; }
  .sub { color: #666; font-size: 0.95em; margin-top: 0; }
  .card { border: 1px solid #ddd; border-radius: 8px; padding: 1em 1.2em; margin: 1em 0; background: #fafafa; }
  label { display: block; padding: 0.4em 0; cursor: pointer; }
  label:hover { background: #f0f0f0; }
  input[type=text], input[type=submit], button { padding: 0.5em 0.8em; font-size: 1em; }
  input[type=submit], button { background: #2b7cd1; color: white; border: 0; border-radius: 4px; cursor: pointer; }
  input[type=submit]:hover, button:hover { background: #1f5fa3; }
  .err { color: #b00020; padding: 0.5em; background: #fde7ea; border-radius: 4px; }
  code { background: #eee; padding: 0.1em 0.3em; border-radius: 3px; font-size: 0.9em; }
  .meta { color: #888; font-size: 0.85em; }
</style>
</head>
<body>
  <h1>Authorize MCP Client</h1>
  <p class="sub">A client wants to connect to your SAGE node as an MCP agent.</p>

  <div class="card">
    <p><strong>Client:</strong> <code>{{.ClientID}}</code></p>
    <p><strong>Redirect URI:</strong> <code>{{.RedirectURI}}</code></p>
    {{if .Scope}}<p><strong>Scope:</strong> <code>{{.Scope}}</code></p>{{end}}
  </div>

  {{if .Error}}<p class="err">{{.Error}}</p>{{end}}

  <form method="POST" action="/oauth/authorize">
    <!-- OAuth params travel as hidden inputs, NOT as a query-string suffix on
         the action URL. html/template treats the action attribute as URL
         context and would double-encode an interpolated raw query string, so
         we use hidden inputs instead. Submission posts them as
         application/x-www-form-urlencoded which the POST handler reads via
         r.FormValue. (Note: keep template actions out of HTML comments —
         html/template parses through comments and a stale field reference in
         a comment will fail Execute even though the comment never reaches
         the browser.) -->
    <input type="hidden" name="client_id" value="{{.ClientID}}">
    <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
    <input type="hidden" name="response_type" value="{{.ResponseType}}">
    <input type="hidden" name="scope" value="{{.Scope}}">
    <input type="hidden" name="state" value="{{.State}}">
    <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
    <input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}">
    <h2 style="font-size: 1.1em;">Mint a new bearer for this client</h2>
    <p class="meta">A new <code>mcp_tokens</code> row is created for this connection — you can revoke it at any time from <code>sage-gui mcp-token revoke &lt;id&gt;</code> or the dashboard.</p>
    <p>
      <label>Token name (optional, e.g. "chatgpt-laptop")<br>
        <input type="text" name="token_name" value="" style="width: 100%; box-sizing: border-box;">
      </label>
    </p>
    <p>
      <label>Run as agent
        {{if .Agents}}
        <br><select name="agent_id" required style="width: 100%; box-sizing: border-box; padding: 0.5em;">
          {{range .Agents}}
          <option value="{{.AgentID}}"{{if eq .AgentID $.DefaultAgentID}} selected{{end}}>
            {{.Name}} ({{.Role}}{{if .RegisteredName}} · {{.RegisteredName}}{{end}}) — {{slice .AgentID 0 8}}…
          </option>
          {{end}}
        </select>
        <span class="meta">ChatGPT will act as this agent. Pick admin for full access.</span>
        {{else}}
        <br><input type="text" name="agent_id" required placeholder="64 hex chars" style="width: 100%; box-sizing: border-box;">
        <span class="meta">No active agents found. Paste a 64-char hex ed25519 pubkey.</span>
        {{end}}
      </label>
    </p>
    <p><button type="submit">Authorize</button></p>
  </form>

  <p class="meta">After authorization, you'll be redirected to {{.RedirectURI}}.<br>
  This consent screen is a thin OAuth wrapper around SAGE's existing bearer-token MCP transport.</p>
</body>
</html>`))

// renderConsent writes the GET-side HTML form.
func (h *OAuthHandler) renderConsent(w http.ResponseWriter, r *http.Request, p authorizeFormParams, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Build the agent dropdown list. Pull active agents from the store, sort
	// admins first so the most-privileged shows at top, and pre-select the
	// first admin as default. If listing fails (e.g. corrupt store), fall
	// through with empty list — the template degrades to the raw text input.
	var (
		visibleAgents  []consentAgent
		defaultAgentID string
	)
	if all, err := h.Store.ListAgents(r.Context()); err == nil {
		var admins, others []consentAgent
		for _, a := range all {
			if a == nil || a.Status != "active" {
				continue
			}
			ca := consentAgent{
				AgentID:        a.AgentID,
				Name:           a.Name,
				Role:           a.Role,
				RegisteredName: a.RegisteredName,
			}
			if a.Role == "admin" {
				admins = append(admins, ca)
			} else {
				others = append(others, ca)
			}
		}
		visibleAgents = append(admins, others...)
		if len(visibleAgents) > 0 {
			defaultAgentID = visibleAgents[0].AgentID
		}
	}

	// Preserve the inbound query so the POST round-trip can read the same
	// PKCE / redirect parameters back out.
	data := struct {
		ClientID            string
		RedirectURI         string
		Scope               string
		State               string
		ResponseType        string
		CodeChallenge       string
		CodeChallengeMethod string
		Error               string
		Agents              []consentAgent
		DefaultAgentID      string
	}{
		ClientID:            p.ClientID,
		RedirectURI:         p.RedirectURI,
		Scope:               p.Scope,
		State:               p.State,
		ResponseType:        p.ResponseType,
		CodeChallenge:       p.CodeChallenge,
		CodeChallengeMethod: p.CodeChallengeMethod,
		Error:               errMsg,
		Agents:              visibleAgents,
		DefaultAgentID:      defaultAgentID,
	}
	if err := consentTemplate.Execute(w, data); err != nil {
		// Surface the real template error to logs so we can diagnose render
		// failures (the headers may already be flushed at this point — the
		// http.Error WriteHeader will be a superfluous-call warning, but the
		// log line is the only place the cause is visible).
		log.Printf("oauth: consentTemplate.Execute failed: %v (data=%+v)", err, data)
		http.Error(w, "failed to render consent page", http.StatusInternalServerError)
	}
}

// processConsent handles the POST: mint a bearer for the chosen agent, bind
// it to a fresh authorization code, and 302-redirect the user back to the
// client's redirect_uri carrying the code (and original state).
func (h *OAuthHandler) processConsent(w http.ResponseWriter, r *http.Request, p authorizeFormParams) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16) // 64KB cap on consent form bodies
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form body", http.StatusBadRequest)
		return
	}
	agentID := strings.TrimSpace(r.FormValue("agent_id"))
	tokenName := strings.TrimSpace(r.FormValue("token_name"))
	if tokenName == "" {
		tokenName = "oauth-" + p.ClientID
	}

	if len(agentID) != 64 {
		h.renderConsent(w, r, p, "agent_id must be a 64-char hex-encoded ed25519 public key")
		return
	}
	if _, decErr := hex.DecodeString(agentID); decErr != nil {
		h.renderConsent(w, r, p, "agent_id must be hex-encoded")
		return
	}

	// 1. Mint a brand-new bearer via the same code path /v1/mcp/tokens uses.
	bearerRaw := make([]byte, 32)
	if _, err := rand.Read(bearerRaw); err != nil {
		http.Error(w, "failed to generate token", http.StatusInternalServerError)
		return
	}
	bearerPlaintext := base64.RawURLEncoding.EncodeToString(bearerRaw)
	digest := sha256.Sum256([]byte(bearerPlaintext))
	digestHex := hex.EncodeToString(digest[:])
	tokenID := uuid.NewString()
	if err := h.Store.InsertMCPToken(r.Context(), tokenID, tokenName, agentID, digestHex); err != nil {
		http.Error(w, "failed to persist token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 2. Mint the auth code and bind it to the bearer plaintext.
	codeRaw := make([]byte, 32)
	if _, err := rand.Read(codeRaw); err != nil {
		http.Error(w, "failed to generate auth code", http.StatusInternalServerError)
		return
	}
	code := base64.RawURLEncoding.EncodeToString(codeRaw)
	if err := h.Store.IssueAuthCode(r.Context(),
		code, tokenID, p.CodeChallenge, p.CodeChallengeMethod,
		p.RedirectURI, p.ClientID, p.State, bearerPlaintext, AuthCodeTTL); err != nil {
		http.Error(w, "failed to issue auth code: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 3. Redirect back to the client's redirect_uri with the code + state.
	redirectURL, err := url.Parse(p.RedirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	q := redirectURL.Query()
	q.Set("code", code)
	if p.State != "" {
		q.Set("state", p.State)
	}
	redirectURL.RawQuery = q.Encode()
	http.Redirect(w, r, redirectURL.String(), http.StatusFound)
}

// HandleToken implements POST /oauth/token. Form-encoded body per OAuth 2.0
// §4.1.3:
//
//	grant_type=authorization_code&code=...&redirect_uri=...&code_verifier=...&client_id=...
//
// On success returns:
//
//	{"access_token": "<bearer>", "token_type": "Bearer", "expires_in": 0}
//
// expires_in=0 = does not expire — the underlying mcp_tokens revocation
// status is the real lifetime gate, and the OAuth spec does NOT require a
// non-zero value.
func (h *OAuthHandler) HandleToken(w http.ResponseWriter, r *http.Request) {
	// Trace logging — OAuth routes don't pass through the JSON request
	// logger, so without these prints we can't tell from the log whether
	// ChatGPT (or any other client) ever made it to /oauth/token.
	log.Printf("oauth: /oauth/token hit method=%s remote=%s ua=%q ct=%q origin=%q",
		r.Method, r.RemoteAddr, r.Header.Get("User-Agent"),
		r.Header.Get("Content-Type"), r.Header.Get("Origin"))

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16) // 64KB cap on /token form bodies
	if err := r.ParseForm(); err != nil {
		log.Printf("oauth: /oauth/token form parse failed: %v", err)
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
		return
	}

	grantType := r.FormValue("grant_type")
	if grantType != "authorization_code" {
		log.Printf("oauth: /oauth/token unsupported grant_type=%q", grantType)
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"only authorization_code is supported")
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	codeVerifier := strings.TrimSpace(r.FormValue("code_verifier"))
	redirectURI := strings.TrimSpace(r.FormValue("redirect_uri"))
	clientID := strings.TrimSpace(r.FormValue("client_id"))
	if code == "" || codeVerifier == "" || redirectURI == "" {
		log.Printf("oauth: /oauth/token missing required fields code_present=%v verifier_present=%v redirect_present=%v client_id=%q",
			code != "", codeVerifier != "", redirectURI != "", clientID)
		writeOAuthError(w, http.StatusBadRequest, "invalid_request",
			"code, code_verifier, redirect_uri are required")
		return
	}
	log.Printf("oauth: /oauth/token redeeming code=%s... redirect_uri=%s client_id=%s verifier_len=%d",
		safePrefix(code, 8), redirectURI, clientID, len(codeVerifier))

	bearer, err := h.Store.RedeemAuthCode(r.Context(), code, codeVerifier, redirectURI)
	if err != nil {
		log.Printf("oauth: /oauth/token redeem failed: %v", err)
		switch {
		case errors.Is(err, store.ErrAuthCodeNotFound),
			errors.Is(err, store.ErrAuthCodeUsed),
			errors.Is(err, store.ErrAuthCodeExpired):
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", err.Error())
		case errors.Is(err, store.ErrAuthCodeRedirectMismatch):
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant",
				"redirect_uri does not match the authorization request")
		case errors.Is(err, store.ErrAuthCodePKCEMismatch):
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant",
				"PKCE code_verifier does not match code_challenge")
		default:
			writeOAuthError(w, http.StatusInternalServerError, "server_error",
				"failed to redeem authorization code")
		}
		return
	}
	log.Printf("oauth: /oauth/token success — bearer issued for code=%s...", safePrefix(code, 8))

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": bearer,
		"token_type":   "Bearer",
		"expires_in":   0,
		"scope":        "mcp",
	})
}

// writeOAuthError emits an RFC 6749 §5.2 error response. The body is JSON
// with `error` + `error_description` fields.
func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": description,
	})
}

// MountOAuthRoutes wires the OAuth endpoints onto a chi-compatible router.
// Caller is responsible for mounting at the host root (NOT under /v1/mcp/).
//
// All endpoints get a CORS shim — ChatGPT's MCP connector does an OPTIONS
// preflight on /oauth/token (and sometimes /oauth/authorize) before
// initiating the user-visible auth flow. Without `Access-Control-Allow-Origin`
// in the preflight response the browser silently rejects the response and
// the connector hangs at "Continue" with a never-resolving spinner. These
// endpoints don't read cookies (token: bearer in body; authorize: query+
// form params + a dashboard cookie that's only checked AFTER the form is
// rendered), so wildcard Origin doesn't open any CSRF vector.
func MountOAuthRoutes(r interface {
	Get(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
	Options(pattern string, h http.HandlerFunc)
}, h *OAuthHandler) {
	wrap := oauthCORSMiddleware
	r.Get("/.well-known/oauth-authorization-server", wrap(h.HandleDiscovery))
	r.Options("/.well-known/oauth-authorization-server", wrap(oauthPreflightHandler))
	r.Get("/.well-known/oauth-protected-resource", wrap(h.HandleProtectedResource))
	r.Options("/.well-known/oauth-protected-resource", wrap(oauthPreflightHandler))
	r.Post("/oauth/register", wrap(h.HandleClientRegister))
	r.Options("/oauth/register", wrap(oauthPreflightHandler))
	r.Get("/oauth/authorize", wrap(h.HandleAuthorize))
	r.Post("/oauth/authorize", wrap(h.HandleAuthorize))
	r.Options("/oauth/authorize", wrap(oauthPreflightHandler))
	r.Post("/oauth/token", wrap(h.HandleToken))
	r.Options("/oauth/token", wrap(oauthPreflightHandler))
}

// consentAgent is the slim view of an agent shown in the consent dropdown.
// We deliberately avoid leaking org/clearance/bundle paths into the public
// consent page — name + role + first-8-of-id is enough to disambiguate.
type consentAgent struct {
	AgentID        string
	Name           string
	Role           string
	RegisteredName string
}

// safePrefix returns the first n bytes of s — for logging code values
// without leaking the full secret. Returns "" for empty input.
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// oauthCORSMiddleware is a passthrough — CORS is handled by the parent
// chi/cors middleware (see node.go), which echoes the origin back when it
// matches the allowlist. We deliberately do NOT set Access-Control-Allow-
// Origin here: writing `*` would override the parent's per-origin echo and
// produce a preflight/actual-response ACAO mismatch (preflight was
// origin-specific, actual was wildcard) that some browsers and OAuth
// clients reject — which is exactly the bug v6.7.5 hit, where ChatGPT
// completed consent but never POSTed /oauth/token.
func oauthCORSMiddleware(h http.HandlerFunc) http.HandlerFunc {
	return h
}

// oauthPreflightHandler is the no-op OPTIONS responder — the middleware
// above has already written the CORS headers.
func oauthPreflightHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

