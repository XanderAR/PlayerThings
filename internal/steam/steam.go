// Package steam talks to the official Steam Web API, the Steam store's
// public app details, and Steam's OpenID sign-in.
package steam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrBudget is returned when the daily Web API call budget is used up.
var ErrBudget = errors.New("steam daily API budget used up")

// ErrBadData means the store answered with something unreadable for one app.
// Asking again won't help, unlike a network error or rate limit.
var ErrBadData = errors.New("steam store sent unreadable data")

// ErrNoKey is returned by Web API calls when no API key is configured.
// Sign-in and store lookups don't need a key, so they keep working.
var ErrNoKey = errors.New("STEAM_API_KEY is not set")

// Client calls Steam. Create it with New.
type Client struct {
	APIKey    string
	APIBase   string // https://api.steampowered.com
	StoreBase string // https://store.steampowered.com
	OpenIDURL string // https://steamcommunity.com/openid/login
	HTTP      *http.Client

	api    *Limiter
	store  *Limiter
	budget *Budget
}

// New returns a client that paces Web API calls to one per second (well under
// Valve's 100,000 a day) and store lookups to one every 1.6 seconds.
func New(apiKey string, dailyBudget int) *Client {
	return &Client{
		APIKey:    apiKey,
		APIBase:   "https://api.steampowered.com",
		StoreBase: "https://store.steampowered.com",
		OpenIDURL: "https://steamcommunity.com/openid/login",
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		api:       NewLimiter(time.Second),
		store:     NewLimiter(1600 * time.Millisecond),
		budget:    NewBudget(dailyBudget),
	}
}

// OwnedGame is one game from GetOwnedGames.
type OwnedGame struct {
	AppID           int    `json:"appid"`
	Name            string `json:"name"`
	PlaytimeForever int    `json:"playtime_forever"` // minutes
	Playtime2Weeks  int    `json:"playtime_2weeks"`  // minutes
	IconHash        string `json:"img_icon_url"`
	LastPlayed      int64  `json:"rtime_last_played"`
}

// Library is a user's owned games.
type Library struct {
	// Private is true when Steam returned no game list, which happens when
	// the user's "Game details" privacy setting isn't public.
	Private bool
	Games   []OwnedGame
}

// OwnedGames fetches a user's library with playtime.
func (c *Client) OwnedGames(ctx context.Context, steamID string) (Library, error) {
	q := url.Values{}
	q.Set("steamid", steamID)
	q.Set("include_appinfo", "true")
	q.Set("include_played_free_games", "true")
	var body struct {
		Response struct {
			GameCount *int        `json:"game_count"`
			Games     []OwnedGame `json:"games"`
		} `json:"response"`
	}
	if err := c.apiGet(ctx, "/IPlayerService/GetOwnedGames/v1/", q, &body); err != nil {
		return Library{}, err
	}
	if body.Response.GameCount == nil {
		return Library{Private: true}, nil
	}
	return Library{Games: body.Response.Games}, nil
}

// Player is a public Steam profile summary.
type Player struct {
	SteamID     string `json:"steamid"`
	PersonaName string `json:"personaname"`
	ProfileURL  string `json:"profileurl"`
	AvatarFull  string `json:"avatarfull"`
	Visibility  int    `json:"communityvisibilitystate"` // 3 = public
}

// PlayerSummary fetches a user's name, avatar and profile URL.
func (c *Client) PlayerSummary(ctx context.Context, steamID string) (Player, error) {
	q := url.Values{}
	q.Set("steamids", steamID)
	var body struct {
		Response struct {
			Players []Player `json:"players"`
		} `json:"response"`
	}
	if err := c.apiGet(ctx, "/ISteamUser/GetPlayerSummaries/v2/", q, &body); err != nil {
		return Player{}, err
	}
	if len(body.Response.Players) == 0 {
		return Player{}, fmt.Errorf("steam profile %s not found", steamID)
	}
	return body.Response.Players[0], nil
}

// Store category IDs that mean people can play together online.
var multiplayerCategories = map[int]bool{
	1:  true, // Multi-player
	9:  true, // Co-op
	27: true, // Cross-Platform Multiplayer
	36: true, // Online PvP
	38: true, // Online Co-op
	49: true, // PvP
}

// Details is store data about a game, from one appdetails call.
type Details struct {
	Multiplayer bool
	Genres      []string
	Reviews     int // Steam "recommendations" total; 0 when the store shows none
	ReleaseYear int // 0 when unknown or not released yet
}

// GameDetails looks up a game's store categories, genres, review count and
// release year. known is false when the store has no page for the app
// (removed, region-locked, or not a game).
func (c *Client) GameDetails(ctx context.Context, appID string) (Details, bool, error) {
	if err := c.store.Wait(ctx); err != nil {
		return Details{}, false, err
	}
	q := url.Values{}
	q.Set("appids", appID)
	q.Set("filters", "categories,genres,recommendations,release_date")
	q.Set("l", "english")
	raw, err := c.get(ctx, c.StoreBase+"/api/appdetails?"+q.Encode())
	if err != nil {
		return Details{}, false, err
	}
	var body map[string]struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return Details{}, false, fmt.Errorf("%w: appdetails: %v", ErrBadData, err)
	}
	entry, ok := body[appID]
	if !ok || !entry.Success {
		return Details{}, false, nil
	}
	// With filters and no matching data the store returns "data": [].
	if len(entry.Data) == 0 || entry.Data[0] != '{' {
		return Details{}, true, nil
	}
	var data struct {
		Categories []struct {
			ID int `json:"id"`
		} `json:"categories"`
		Genres []struct {
			Description string `json:"description"`
		} `json:"genres"`
		Recommendations struct {
			Total int `json:"total"`
		} `json:"recommendations"`
		ReleaseDate struct {
			Date string `json:"date"`
		} `json:"release_date"`
	}
	if err := json.Unmarshal(entry.Data, &data); err != nil {
		return Details{}, false, fmt.Errorf("%w: appdetails data: %v", ErrBadData, err)
	}
	d := Details{Reviews: data.Recommendations.Total, ReleaseYear: releaseYear(data.ReleaseDate.Date)}
	for _, cat := range data.Categories {
		if multiplayerCategories[cat.ID] {
			d.Multiplayer = true
			break
		}
	}
	for _, g := range data.Genres {
		if name := strings.TrimSpace(g.Description); name != "" {
			d.Genres = append(d.Genres, name)
		}
	}
	return d, true, nil
}

// IsMultiplayer reports whether a game supports online play. known is false
// when the store has no page for the app.
func (c *Client) IsMultiplayer(ctx context.Context, appID string) (multiplayer, known bool, err error) {
	d, known, err := c.GameDetails(ctx, appID)
	return d.Multiplayer, known, err
}

var yearPattern = regexp.MustCompile(`\b(19[89]\d|20\d\d|2100)\b`)

// releaseYear pulls the year out of a store release date, whose format varies
// by game and language ("13 May, 2020", "May 13, 2020", "Q1 2027"). It
// returns 0 for dates without a year, such as "Coming soon".
func releaseYear(date string) int {
	years := yearPattern.FindAllString(date, -1)
	if len(years) == 0 {
		return 0
	}
	y, err := strconv.Atoi(years[len(years)-1])
	if err != nil {
		return 0
	}
	return y
}

func (c *Client) apiGet(ctx context.Context, path string, q url.Values, out any) error {
	if c.APIKey == "" {
		return ErrNoKey
	}
	if !c.budget.Take() {
		return ErrBudget
	}
	if err := c.api.Wait(ctx); err != nil {
		return err
	}
	q.Set("key", c.APIKey)
	q.Set("format", "json")
	raw, err := c.get(ctx, c.APIBase+path+"?"+q.Encode())
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, redactKey(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("steam %s: %s", req.URL.Path, resp.Status)
	}
	return body, nil
}

var keyParam = regexp.MustCompile(`key=[^&\s"]+`)

// redactKey keeps the API key out of logged URL errors.
func redactKey(err error) error {
	return errors.New(keyParam.ReplaceAllString(err.Error(), "key=REDACTED"))
}

// ----- OpenID sign-in -----

const (
	openIDNS         = "http://specs.openid.net/auth/2.0"
	identifierSelect = "http://specs.openid.net/auth/2.0/identifier_select"
	steamOPEndpoint  = "https://steamcommunity.com/openid/login"
)

var claimedIDPattern = regexp.MustCompile(`^https://steamcommunity\.com/openid/id/(7656119\d{10})$`)

// LoginURL is where to send the user to sign in through Steam.
func (c *Client) LoginURL(returnTo, realm string) string {
	v := url.Values{}
	v.Set("openid.ns", openIDNS)
	v.Set("openid.mode", "checkid_setup")
	v.Set("openid.return_to", returnTo)
	v.Set("openid.realm", realm)
	v.Set("openid.identity", identifierSelect)
	v.Set("openid.claimed_id", identifierSelect)
	return c.OpenIDURL + "?" + v.Encode()
}

// VerifyLogin checks Steam's OpenID callback and returns the SteamID64.
// expectedReturnTo must be exactly the return_to passed to LoginURL.
func (c *Client) VerifyLogin(ctx context.Context, query url.Values, expectedReturnTo string) (string, error) {
	if mode := query.Get("openid.mode"); mode != "id_res" {
		return "", fmt.Errorf("steam sign-in was not completed (mode %q)", mode)
	}
	if query.Get("openid.op_endpoint") != steamOPEndpoint {
		return "", errors.New("unexpected OpenID provider")
	}
	if query.Get("openid.return_to") != expectedReturnTo {
		return "", errors.New("OpenID return address does not match")
	}
	claimed := query.Get("openid.claimed_id")
	m := claimedIDPattern.FindStringSubmatch(claimed)
	if m == nil || query.Get("openid.identity") != claimed {
		return "", errors.New("OpenID response has no valid Steam ID")
	}
	signed := "," + query.Get("openid.signed") + ","
	for _, field := range []string{"claimed_id", "identity", "return_to", "response_nonce", "op_endpoint"} {
		if !strings.Contains(signed, ","+field+",") {
			return "", fmt.Errorf("OpenID response does not sign %s", field)
		}
	}

	form := url.Values{}
	for k, vs := range query {
		if strings.HasPrefix(k, "openid.") {
			form[k] = vs
		}
	}
	form.Set("openid.mode", "check_authentication")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.OpenIDURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == "is_valid:true" {
			return m[1], nil
		}
	}
	return "", errors.New("Steam did not confirm the sign-in")
}

// ----- URLs -----

// HeaderImage is the store header art for an app.
func HeaderImage(appID string) string {
	return "https://cdn.cloudflare.steamstatic.com/steam/apps/" + appID + "/header.jpg"
}

// StoreURL is the Steam store page for an app.
func StoreURL(appID string) string {
	return "https://store.steampowered.com/app/" + appID
}

// ----- pacing -----

// Limiter spaces calls at least interval apart.
type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

// NewLimiter returns a limiter allowing one call per interval.
func NewLimiter(interval time.Duration) *Limiter { return &Limiter{interval: interval} }

// Wait blocks until the next call is allowed.
func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil || l.interval <= 0 {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	wait := l.next.Sub(now)
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Budget caps calls per UTC day.
type Budget struct {
	mu    sync.Mutex
	limit int
	day   string
	used  int
}

// NewBudget returns a daily budget; limit <= 0 means unlimited.
func NewBudget(limit int) *Budget { return &Budget{limit: limit} }

// Take uses one call, reporting false if the day's budget is spent.
func (b *Budget) Take() bool {
	if b == nil || b.limit <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	today := time.Now().UTC().Format("2006-01-02")
	if today != b.day {
		b.day, b.used = today, 0
	}
	if b.used >= b.limit {
		return false
	}
	b.used++
	return true
}

// Used reports calls made today.
func (b *Budget) Used() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Now().UTC().Format("2006-01-02") != b.day {
		return 0
	}
	return b.used
}

// AppIDString formats an app ID.
func AppIDString(id int) string { return strconv.Itoa(id) }
