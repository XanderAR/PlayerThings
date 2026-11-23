package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"playradar/internal/linktoken"
)

// One-click linking: people who have connected Steam to their Discord account
// approve a Discord sign-in, and the Steam account Discord has verified is
// linked without a Steam sign-in. Discord only shares connections with the
// user's own consent (the "connections" scope), so this is the only way to
// read them. The access token is revoked straight after.

// DiscordOAuth holds the app's OAuth2 settings. It's disabled without a
// client secret.
type DiscordOAuth struct {
	ClientID     string
	ClientSecret string
	API          string // Discord's API base, e.g. https://discord.com/api/v10
	AuthorizeURL string // e.g. https://discord.com/oauth2/authorize
	HTTP         *http.Client
}

// NewDiscordOAuth returns settings pointing at Discord.
func NewDiscordOAuth(clientID, clientSecret string) *DiscordOAuth {
	return &DiscordOAuth{
		ClientID: clientID, ClientSecret: clientSecret,
		API:          "https://discord.com/api/v10",
		AuthorizeURL: "https://discord.com/oauth2/authorize",
		HTTP:         &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled reports whether Discord linking can be offered.
func (d *DiscordOAuth) Enabled() bool { return d != nil && d.ClientID != "" && d.ClientSecret != "" }

// connection is one third-party account on a Discord profile.
type connection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Verified bool   `json:"verified"`
	Revoked  bool   `json:"revoked"`
}

func (d *DiscordOAuth) exchange(ctx context.Context, code, redirect string) (string, error) {
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirect},
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := d.form(ctx, "/oauth2/token", form, &body); err != nil {
		return "", err
	}
	if body.AccessToken == "" {
		return "", errors.New("discord sent no access token")
	}
	return body.AccessToken, nil
}

// revoke ends the access token: nothing else is ever read with it.
func (d *DiscordOAuth) revoke(token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.form(ctx, "/oauth2/token/revoke", url.Values{"token": {token}, "token_type_hint": {"access_token"}}, nil)
}

func (d *DiscordOAuth) form(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.API+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(d.ClientID, d.ClientSecret)
	return d.do(req, out)
}

func (d *DiscordOAuth) get(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.API+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return d.do(req, out)
}

func (d *DiscordOAuth) do(req *http.Request, out any) error {
	resp, err := d.HTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL can carry a code; keep it out of logs
		}
		return fmt.Errorf("discord %s: %w", req.URL.Path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord %s: %s", req.URL.Path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (s *Server) discordRedirectURL() string { return s.BaseURL + "/link/discord/callback" }

// discordStart sends someone to Discord's consent screen. The link token
// rides along as the OAuth state, so the callback knows who ran /link.
func (s *Server) discordStart(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	if _, err := linktoken.Verify(s.LinkSecret, token, s.now()); err != nil {
		s.tokenProblem(w, err)
		return
	}
	if !s.Discord.Enabled() {
		http.Redirect(w, r, "/link?t="+url.QueryEscape(token), http.StatusFound)
		return
	}
	q := url.Values{
		"client_id":     {s.Discord.ClientID},
		"response_type": {"code"},
		"redirect_uri":  {s.discordRedirectURL()},
		"scope":         {"identify connections"},
		"state":         {token},
		"prompt":        {"none"}, // skip the consent screen if they've approved before
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, s.Discord.AuthorizeURL+"?"+q.Encode(), http.StatusFound)
}

// discordCallback links the verified Steam account on the person's Discord.
func (s *Server) discordCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	token := q.Get("state")
	claims, err := linktoken.Verify(s.LinkSecret, token, s.now())
	if err != nil {
		s.tokenProblem(w, err)
		return
	}
	steamInstead := map[string]any{
		"ActionURL":   "/link?t=" + url.QueryEscape(token),
		"ActionLabel": "Sign in through Steam instead",
	}
	page := func(status int, title, body string) {
		data := map[string]any{"Title": title, "Body": body}
		for k, v := range steamInstead {
			data[k] = v
		}
		s.render(w, status, "result", data)
	}
	if q.Get("error") != "" || q.Get("code") == "" || !s.Discord.Enabled() {
		page(http.StatusOK, "Discord sign-in didn't finish", "Nothing was linked.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	access, err := s.Discord.exchange(ctx, q.Get("code"), s.discordRedirectURL())
	if err != nil {
		s.log().Warn("discord token exchange", "err", err)
		page(http.StatusBadGateway, "Discord didn't answer", "Nothing was linked. Try again, or sign in through Steam.")
		return
	}
	defer s.Discord.revoke(access)

	var me struct {
		ID string `json:"id"`
	}
	if err := s.Discord.get(ctx, access, "/users/@me", &me); err != nil {
		s.log().Warn("discord user", "err", err)
		page(http.StatusBadGateway, "Discord didn't answer", "Nothing was linked. Try again, or sign in through Steam.")
		return
	}
	if me.ID != claims.DiscordID {
		// The link was made by someone else: never link across accounts.
		s.render(w, http.StatusForbidden, "result", map[string]any{
			"Title": "This link is for a different Discord account",
			"Body":  "Nothing was linked. Run /link from your own Discord account.",
		})
		return
	}
	var conns []connection
	if err := s.Discord.get(ctx, access, "/users/@me/connections", &conns); err != nil {
		s.log().Warn("discord connections", "err", err)
		page(http.StatusBadGateway, "Discord didn't answer", "Nothing was linked. Try again, or sign in through Steam.")
		return
	}
	steamID := ""
	for _, c := range conns {
		if c.Type == "steam" && c.Verified && !c.Revoked && isSteamID(c.ID) {
			steamID = c.ID
			break
		}
	}
	if steamID == "" {
		page(http.StatusOK, "No Steam account on your Discord",
			"Connect Steam in Discord under User Settings → Connections and try again, or sign in through Steam instead.")
		return
	}
	s.finishLink(w, r, claims, steamID)
}

// isSteamID reports whether s looks like a SteamID64.
func isSteamID(s string) bool {
	if len(s) != 17 || !strings.HasPrefix(s, "7656") {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
