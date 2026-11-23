package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Steam keeps newer apps' art under hashed paths
// (".../apps/<id>/<hash>/library_capsule.jpg"), so the fixed URLs that work
// for older games 404 for them. The store's IStoreBrowseService says where
// each app's art really is; it needs no API key.

// Art kinds served at /render/art.jpg: a portrait cover or the wide header.
const (
	ArtCover  = "cover"
	ArtHeader = "header"
)

// ValidArt reports whether kind is an art kind the endpoint serves.
func ValidArt(kind string) bool { return kind == ArtCover || kind == ArtHeader }

// ArtURL is the public, signed URL of one app's art, served through this app
// so it works for games whose art lives under hashed paths.
func ArtURL(site string, secret []byte, appID, kind string) string {
	return fmt.Sprintf("%s/render/art.jpg?a=%s&k=%s&s=%s", site, appID, kind, Sign(secret, "art-"+kind, appID))
}

// VerifyArt checks an ArtURL signature.
func VerifyArt(secret []byte, appID, kind, sig string) bool {
	return digits(appID) && len(appID) <= 12 && ValidArt(kind) && Verify(secret, "art-"+kind, appID, sig)
}

const (
	steamCDN        = "https://shared.akamai.steamstatic.com/store_item_assets/"
	missRetry       = time.Hour     // how long a failed art URL isn't tried again
	assetsRetry     = 6 * time.Hour // how long an app with no known art isn't looked up again
	maxRawArt       = 300           // raw art files kept for /render/art.jpg
	maxAssetEntries = 5000
)

type storeAssets struct {
	cover, header string
	at            time.Time
}

// SteamAssets asks the Steam store for an app's current cover and header URLs.
func (r *Renderer) SteamAssets(ctx context.Context, appID string) (cover, header string, err error) {
	input, _ := json.Marshal(map[string]any{
		"ids":          []map[string]any{{"appid": json.Number(appID)}},
		"context":      map[string]string{"language": "english", "country_code": "US"},
		"data_request": map[string]bool{"include_assets": true},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.steampowered.com/IStoreBrowseService/GetItems/v1/?input_json="+url.QueryEscape(string(input)), nil)
	if err != nil {
		return "", "", err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("store assets: %s", resp.Status)
	}
	var body struct {
		Response struct {
			StoreItems []struct {
				Assets struct {
					Format  string `json:"asset_url_format"`
					Library string `json:"library_capsule"`
					Header  string `json:"header"`
				} `json:"assets"`
			} `json:"store_items"`
		} `json:"response"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", "", err
	}
	if len(body.Response.StoreItems) == 0 {
		return "", "", nil
	}
	a := body.Response.StoreItems[0].Assets
	build := func(file string) string {
		if a.Format == "" || file == "" || !strings.Contains(a.Format, "${FILENAME}") {
			return ""
		}
		return steamCDN + strings.Replace(a.Format, "${FILENAME}", file, 1)
	}
	return build(a.Library), build(a.Header), nil
}

// assetsFor is SteamAssets with a cache, including of apps with no art.
func (r *Renderer) assetsFor(ctx context.Context, appID string) storeAssets {
	if r.Assets == nil {
		return storeAssets{}
	}
	r.mu.Lock()
	a, ok := r.assets[appID]
	r.mu.Unlock()
	if ok && (a.cover != "" || a.header != "" || time.Since(a.at) < assetsRetry) {
		return a
	}
	cover, header, err := r.Assets(ctx, appID)
	if err != nil {
		return storeAssets{} // try again next time; it may be a blip
	}
	a = storeAssets{cover: cover, header: header, at: time.Now()}
	r.mu.Lock()
	if len(r.assets) > maxAssetEntries {
		r.assets = map[string]storeAssets{}
	}
	r.assets[appID] = a
	r.mu.Unlock()
	return a
}

// artChain lists where to look for an app's art, most likely first. The
// store is only asked when the fixed URLs fail.
func (r *Renderer) artChain(ctx context.Context, appID, kind string, try func(string) bool) bool {
	first, second := r.Cover(appID), r.Fallback(appID)
	if kind == ArtHeader {
		first, second = second, ""
	}
	for _, u := range []string{first, second} {
		if u != "" && try(u) {
			return true
		}
	}
	a := r.assetsFor(ctx, appID)
	order := []string{a.cover, a.header}
	if kind == ArtHeader {
		order = []string{a.header, a.cover}
	}
	for _, u := range order {
		if u != "" && try(u) {
			return true
		}
	}
	return false
}

// Art returns one app's art file (a cover or header) and its content type.
func (r *Renderer) Art(ctx context.Context, appID, kind string) ([]byte, string, error) {
	if err := r.init(); err != nil {
		return nil, "", err
	}
	key := kind + "|" + appID
	r.mu.Lock()
	if raw, ok := r.raw[key]; ok {
		r.mu.Unlock()
		return raw, http.DetectContentType(raw), nil
	}
	r.mu.Unlock()
	var found []byte
	r.artChain(ctx, appID, kind, func(u string) bool {
		raw, err := r.fetchRaw(ctx, u)
		if err == nil {
			found = raw
		}
		return err == nil
	})
	if found == nil {
		return nil, "", errors.New("no art for this app")
	}
	r.mu.Lock()
	if len(r.raw) > maxRawArt {
		r.raw = map[string][]byte{}
	}
	r.raw[key] = found
	r.mu.Unlock()
	return found, http.DetectContentType(found), nil
}

// fetchRaw downloads an image file, remembering failures for a while so a
// missing file isn't requested on every render.
func (r *Renderer) fetchRaw(ctx context.Context, u string) ([]byte, error) {
	r.mu.Lock()
	if at, ok := r.missing[u]; ok && time.Since(at) < missRetry {
		r.mu.Unlock()
		return nil, errors.New("recently missing")
	}
	r.mu.Unlock()
	raw, err := r.download(ctx, u)
	if err != nil && ctx.Err() == nil {
		r.mu.Lock()
		if len(r.missing) > maxAssetEntries {
			r.missing = map[string]time.Time{}
		}
		r.missing[u] = time.Now()
		r.mu.Unlock()
	}
	return raw, err
}

func (r *Renderer) download(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(http.DetectContentType(raw), "image/") || bytes.HasPrefix(raw, []byte("<")) {
		return nil, fmt.Errorf("%s: not an image", u)
	}
	return raw, nil
}
