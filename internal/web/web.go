// Package web serves the small website around the bot: the Steam linking
// flow, the click-counting redirect to player.gg, and the privacy and terms
// pages Discord and Valve require.
package web

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"playradar/internal/linktoken"
	"playradar/internal/render"
	"playradar/internal/store"
)

// iconPNG is the app icon, used as the avatar on scheduled webhook posts.
//
//go:embed static/icon.png
var iconPNG []byte

// SteamAuth is Steam's OpenID sign-in.
type SteamAuth interface {
	LoginURL(returnTo, realm string) string
	VerifyLogin(ctx context.Context, query url.Values, expectedReturnTo string) (string, error)
}

// Syncer refreshes a linked account.
type Syncer interface {
	Sync(ctx context.Context, discordID, platform string) (store.Profile, int, error)
}

// Server holds what the pages need. Fill every field before serving.
type Server struct {
	AppName      string
	BaseURL      string
	Contact      string
	InstallURL   string
	PlayerGGURL  string
	Store        *store.Store
	Steam        SteamAuth
	Syncer       Syncer
	LinkSecret   []byte
	Interactions http.Handler
	Render       *render.Renderer // draws cover shelves; nil disables /render
	Discord      *DiscordOAuth    // one-click linking from Discord connections; nil or no secret disables it
	Log          *slog.Logger
	Now          func() time.Time
}

// Routes returns the HTTP handler for the whole service.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /interactions", s.Interactions)
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /link", s.linkPage)
	mux.HandleFunc("GET /link/steam/callback", s.steamCallback)
	mux.HandleFunc("GET /link/discord", s.discordStart)
	mux.HandleFunc("GET /link/discord/callback", s.discordCallback)
	mux.HandleFunc("GET /go", s.goRedirect)
	mux.HandleFunc("GET /privacy", s.privacy)
	mux.HandleFunc("GET /terms", s.terms)
	mux.HandleFunc("GET /render/shelf.jpg", s.shelf)
	mux.HandleFunc("GET /render/art.jpg", s.art)
	mux.HandleFunc("GET /icon.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(iconPNG)
	})
	mux.Handle("GET /media/", http.StripPrefix("/media/", mediaHandler()))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return securityHeaders(mux)
}

// shelf draws a signed cover shelf. The URL holds everything needed, so the
// image never changes and Discord can cache it for good.
func (s *Server) shelf(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	layout, games, sig := q.Get("l"), q.Get("g"), q.Get("s")
	if s.Render == nil || !render.ValidLayout(layout) || !render.Verify(s.LinkSecret, layout, games, sig) {
		http.Error(w, "unknown image", http.StatusForbidden)
		return
	}
	tiles, err := render.Decode(games)
	if err != nil {
		http.Error(w, "unknown image", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	img, complete, err := s.Render.Shelf(ctx, layout, tiles)
	if err != nil {
		s.serverError(w, "render shelf", err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	if complete {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// Some art was missing (often a Steam hiccup): let caches ask again soon.
		w.Header().Set("Cache-Control", "public, max-age=600")
	}
	w.Write(img)
}

// art serves one game's cover or header. Steam keeps newer games' art under
// hashed paths, so cards link here instead of guessing Steam URLs.
func (s *Server) art(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, kind := q.Get("a"), q.Get("k")
	if s.Render == nil || !render.VerifyArt(s.LinkSecret, id, kind, q.Get("s")) {
		http.Error(w, "unknown image", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	raw, typ, err := s.Render.Art(ctx, id, kind)
	if err != nil {
		http.Error(w, "no art for this game", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", typ)
	// Art can change when a game updates its store page, so not forever.
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(raw)
}

// mediaFiles are the images for listing pages like top.gg (built by
// tools/topgg).
//
//go:embed static/media
var mediaFiles embed.FS

func mediaHandler() http.Handler {
	sub, err := fs.Sub(mediaFiles, "static/media")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r) // no folder listings
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		files.ServeHTTP(w, r)
	})
}

func (s *Server) callbackURL(token string) string {
	return s.BaseURL + "/link/steam/callback?t=" + url.QueryEscape(token)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "home", nil)
}

func (s *Server) linkPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	claims, err := linktoken.Verify(s.LinkSecret, token, s.now())
	if err != nil {
		s.tokenProblem(w, err)
		return
	}
	s.render(w, http.StatusOK, "link", map[string]any{
		"SteamURL": s.Steam.LoginURL(s.callbackURL(token), s.BaseURL),
		"InServer": claims.GuildID != "",
		"For":      claims.Name,
		"Discord":  s.Discord.Enabled(),
		"Token":    token,
	})
}

func (s *Server) steamCallback(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	claims, err := linktoken.Verify(s.LinkSecret, token, s.now())
	if err != nil {
		s.tokenProblem(w, err)
		return
	}
	steamID, err := s.Steam.VerifyLogin(r.Context(), r.URL.Query(), s.callbackURL(token))
	if err != nil {
		s.log().Warn("steam sign-in failed", "err", err)
		s.render(w, http.StatusBadRequest, "result", map[string]any{
			"Title": "Steam sign-in didn't finish",
			"Body":  "Nothing was linked. Run /link in Discord to try again.",
		})
		return
	}
	s.finishLink(w, r, claims, steamID)
}

// finishLink stores a verified Steam account for the Discord user in claims,
// opts them into the server /link was run in, and loads their library.
func (s *Server) finishLink(w http.ResponseWriter, r *http.Request, claims linktoken.Claims, steamID string) {
	ctx := r.Context()
	if err := s.Store.LinkAccount(ctx, store.Account{
		DiscordID: claims.DiscordID, Platform: "steam", ExternalID: steamID, LinkedAt: s.now(),
	}); err != nil {
		s.serverError(w, "link account", err)
		return
	}
	if claims.GuildID != "" {
		if err := s.Store.SetVisibility(ctx, claims.GuildID, claims.DiscordID, true, s.now()); err != nil {
			s.serverError(w, "set visibility", err)
			return
		}
	}

	syncCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	profile, games, err := s.Syncer.Sync(syncCtx, claims.DiscordID, "steam")
	data := map[string]any{"Title": "Steam linked", "Done": true}
	switch {
	case err != nil:
		s.log().Warn("first sync failed", "err", err)
		data["Body"] = "Your account is linked, but your library didn't load yet. It will sync automatically within a few minutes."
	case profile.Private:
		data["Title"] = "Linked as " + profile.DisplayName
		data["Private"] = true
	default:
		data["Title"] = "Linked as " + profile.DisplayName
		data["Body"] = pluralGames(games) + " synced. Head back to Discord and try /profile."
	}
	s.render(w, http.StatusOK, "result", data)
}

// goRedirect counts a click and forwards to player.gg with UTM tags. Only
// player.gg URLs are allowed, so this can't be used as an open redirect.
func (s *Server) goRedirect(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	target, err := url.Parse(q.Get("u"))
	allowed, _ := url.Parse(s.PlayerGGURL)
	if err != nil || allowed == nil || target.Scheme != "https" ||
		(target.Host != allowed.Host && !strings.HasSuffix(target.Host, "."+allowed.Host)) {
		http.Error(w, "unknown link", http.StatusBadRequest)
		return
	}
	// Only links the app made are counted, so nobody can fill the database
	// by requesting made-up ones. Unsigned links still redirect.
	signed := linktoken.VerifyClick(s.LinkSecret, q.Get("u"), q.Get("c"), q.Get("g"), q.Get("s"))
	command := clean(q.Get("c"), 24)
	guildID := clean(q.Get("g"), 24)

	tq := target.Query()
	tq.Set("utm_source", "discord")
	tq.Set("utm_medium", "app")
	if command != "" {
		tq.Set("utm_campaign", command)
	}
	if guildID != "" {
		tq.Set("utm_content", guildID)
	}
	target.RawQuery = tq.Encode()

	if signed {
		if err := s.Store.CountUsage(r.Context(), store.UsageClick, command, guildID, s.now()); err != nil {
			s.log().Warn("count click", "err", err)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (s *Server) privacy(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "privacy", nil)
}

func (s *Server) terms(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "terms", nil)
}

func (s *Server) tokenProblem(w http.ResponseWriter, err error) {
	title := "This link isn't valid"
	if errors.Is(err, linktoken.ErrExpired) {
		title = "This link has expired"
	}
	s.render(w, http.StatusBadRequest, "result", map[string]any{
		"Title": title,
		"Body":  "Run /link in Discord to get a fresh one.",
	})
}

func (s *Server) serverError(w http.ResponseWriter, what string, err error) {
	s.log().Error(what, "err", err)
	s.render(w, http.StatusInternalServerError, "result", map[string]any{
		"Title": "Something went wrong",
		"Body":  "Try /link again in a minute.",
	})
}

func (s *Server) render(w http.ResponseWriter, status int, page string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["App"] = s.AppName
	data["Contact"] = s.Contact
	data["InstallURL"] = s.InstallURL
	data["PlayerGG"] = s.PlayerGGURL
	data["Page"] = page
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, "layout", data); err != nil {
		s.log().Error("render page", "page", page, "err", err)
	}
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// The link token is in the page URL; never send it to Steam as a Referer.
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src https: data:; form-action 'none'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

func clean(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
		if b.Len() >= max {
			break
		}
	}
	return b.String()
}

func pluralGames(n int) string {
	if n == 1 {
		return "1 game"
	}
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s + " games"
}

var pages = template.Must(template.New("pages").Parse(pageTemplates))
