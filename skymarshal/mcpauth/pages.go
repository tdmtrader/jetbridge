package mcpauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strings"
	"time"
)

const pageStyle = `:root{color-scheme:light dark;font:16px/1.55 system-ui,sans-serif}body{margin:0;background:#171b24;color:#eef1f7}main{max-width:42rem;margin:3rem auto;padding:1.5rem}h1{line-height:1.2}p{color:#bbc5d8}label{display:block;padding:.8rem 0;border-bottom:1px solid #354057}small{display:block;margin-left:1.7rem;color:#bbc5d8}input{margin-right:.5rem}button{font:inherit;border:1px solid #697998;border-radius:.4rem;padding:.55rem 1rem;margin:.8rem .5rem .3rem 0;background:#283753;color:#fff;cursor:pointer}button[value=allow]{background:#286649}a{color:#9ac8ff}.grant{border:1px solid #354057;border-radius:.5rem;padding:1rem;margin:1rem 0}code{overflow-wrap:anywhere}footer{margin-top:2rem;color:#bbc5d8}`

var consentTemplate = template.Must(template.New("consent").Parse(`<!doctype html><html lang="en"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorize JetBridge MCP</title><style>` + pageStyle + `</style><main><h1>Connect {{.Client.Name}} to JetBridge</h1><p>Signed in as <strong>{{.UserName}}</strong>.</p><p>This client will return to <code>{{.Redirect}}</code>.</p><p>Choose the capabilities you want this connection to have. They apply to teams your account can access; your current team permissions still apply. Only pipeline status is exposed by the initial MCP tool surface. Other selected capabilities permit future tools in those categories.</p><form method="post" action="{{.Action}}"><input type="hidden" name="request" value="{{.Request}}"><input type="hidden" name="csrf" value="{{.CSRF}}">{{range .Scopes}}<label><input type="checkbox" name="scope" value="{{.Name}}" {{if eq .Name "read"}}checked{{end}}>{{.Label}}<small>{{.Description}}</small></label>{{end}}<p>The connection can renew access for up to {{.AbsoluteLifetime}}, with expiry after {{.IdleLifetime}} of inactivity. You can revoke it at any time.</p><button name="decision" value="allow">Allow selected capabilities</button><button name="decision" value="deny">Deny</button></form><footer><a href="{{.Manage}}">Manage existing connections</a></footer></main></html>`))

func (s *Server) renderConsent(w http.ResponseWriter, id string, req authorizationRequest, client Client) {
	var scopes []Scope
	for _, scope := range Scopes {
		if slices.Contains(req.Scopes, scope.Name) {
			scopes = append(scopes, scope)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = consentTemplate.Execute(w, struct {
		Client                                                                            Client
		Redirect, Action, Request, CSRF, Manage, UserName, IdleLifetime, AbsoluteLifetime string
		Scopes                                                                            []Scope
	}{Client: client, Redirect: req.RedirectURI, Action: s.issuerPath + "/consent", Request: id, CSRF: req.CSRF,
		Manage: s.issuerPath + "/grants", UserName: req.UserName, Scopes: scopes,
		IdleLifetime: durationLabel(s.config.IdleLifetime), AbsoluteLifetime: durationLabel(s.config.AbsoluteLifetime)})
}

func durationLabel(duration time.Duration) string {
	if duration%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d days", int(duration/(24*time.Hour)))
	}
	return duration.String()
}

type GrantInfo struct {
	ID              string    `json:"id"`
	ClientID        string    `json:"client_id"`
	ClientName      string    `json:"client_name"`
	Scopes          []string  `json:"scopes"`
	Created         time.Time `json:"created_at"`
	IdleExpires     time.Time `json:"idle_expires_at"`
	AbsoluteExpires time.Time `json:"absolute_expires_at"`
}

var grantsTemplate = template.Must(template.New("grants").Parse(`<!doctype html><html lang="en"><meta name="viewport" content="width=device-width,initial-scale=1"><title>JetBridge MCP connections</title><style>` + pageStyle + `</style><main><h1>MCP connections</h1><p>These connections act within your current team permissions. Revoking a connection stops its access and renewal immediately.</p>{{range .Grants}}<section class="grant"><strong>{{.ClientName}}</strong><p>{{range .Scopes}}<code>{{.}}</code> {{end}}</p><p>Expires {{.AbsoluteExpires.Format "Jan 2, 2006"}}</p><form method="post" action="{{$.Action}}"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="grant_id" value="{{.ID}}"><button>Revoke connection</button></form></section>{{else}}<p>You have no active MCP connections.</p>{{end}}</main></html>`))

func (s *Server) grants(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(405)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Malformed request", 400)
		return
	}
	wantsJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	var b browserSession
	var list []GrantInfo
	err := s.config.Store.WithTx(r.Context(), func(tx Tx) error {
		cookie, err := r.Cookie("jbm_browser")
		if err != nil {
			return ErrInvalidGrant
		}
		if err := tx.Get("browser", digest(cookie.Value), &b); err != nil {
			return err
		}
		if !s.config.Now().Before(b.Expires) {
			return ErrInvalidGrant
		}
		if r.Method == http.MethodPost {
			if !equal(b.CSRF, r.PostForm.Get("csrf")) {
				return ErrInvalidGrant
			}
			var g grant
			if err := tx.Get("grant", r.PostForm.Get("grant_id"), &g); err != nil {
				return err
			}
			if g.IdentityKey != b.IdentityKey {
				return ErrInvalidGrant
			}
			g.Revoked = true
			return tx.Put("grant", g.ID, g, g.AbsoluteExpires)
		}
		records, err := tx.List("grant")
		if err != nil {
			return err
		}
		for _, record := range records {
			var g grant
			if err := json.Unmarshal(record.Data, &g); err != nil {
				return err
			}
			if g.IdentityKey != b.IdentityKey || !g.valid(s.config.Now()) || g.Resource != s.config.Resource {
				continue
			}
			name := g.ClientID
			if client, ok := s.clients[g.ClientID]; ok {
				name = client.Name
			}
			list = append(list, GrantInfo{ID: g.ID, ClientID: g.ClientID, ClientName: name, Scopes: g.Scopes, Created: g.Created, IdleExpires: g.IdleExpires, AbsoluteExpires: g.AbsoluteExpires})
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalidGrant) || errors.Is(err, ErrNotFound) {
			if r.Method == http.MethodGet && !wantsJSON {
				s.begin(w, r, authorizationRequest{Management: true})
				return
			}
			http.Error(w, "Sign in again or supply the current CSRF token", 403)
			return
		}
		http.Error(w, "Connection storage unavailable", 503)
		return
	}
	if r.Method == http.MethodPost {
		if wantsJSON {
			w.WriteHeader(204)
		} else {
			http.Redirect(w, r, s.issuerPath+"/grants", http.StatusSeeOther)
		}
		return
	}
	if list == nil {
		list = []GrantInfo{}
	}
	if wantsJSON {
		writeJSON(w, 200, map[string]any{"grants": list, "csrf_token": b.CSRF})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = grantsTemplate.Execute(w, struct {
		Grants       []GrantInfo
		CSRF, Action string
	}{list, b.CSRF, s.issuerPath + "/grants"})
}
