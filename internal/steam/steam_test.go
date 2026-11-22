package steam

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("TESTKEY", 0)
	c.APIBase, c.StoreBase, c.OpenIDURL = srv.URL, srv.URL, srv.URL+"/openid/login"
	c.api, c.store = nil, nil // no pacing in tests
	return c
}

func TestOwnedGames(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "TESTKEY" || r.URL.Query().Get("steamid") != "76561198000000001" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		w.Write([]byte(`{"response":{"game_count":2,"games":[
			{"appid":548430,"name":"Deep Rock Galactic","playtime_forever":24720,"playtime_2weeks":360,"rtime_last_played":1763000000},
			{"appid":730,"name":"Counter-Strike 2","playtime_forever":18060}]}}`))
	})
	lib, err := c.OwnedGames(context.Background(), "76561198000000001")
	if err != nil {
		t.Fatal(err)
	}
	if lib.Private || len(lib.Games) != 2 || lib.Games[0].PlaytimeForever != 24720 || lib.Games[0].Playtime2Weeks != 360 {
		t.Fatalf("lib = %+v", lib)
	}
}

func TestOwnedGamesPrivate(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"response":{}}`)) })
	lib, err := c.OwnedGames(context.Background(), "76561198000000001")
	if err != nil || !lib.Private {
		t.Fatalf("lib = %+v, err = %v", lib, err)
	}
}

func TestPublicButEmptyLibraryIsNotPrivate(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"response":{"game_count":0}}`)) })
	lib, err := c.OwnedGames(context.Background(), "76561198000000001")
	if err != nil || lib.Private {
		t.Fatalf("lib = %+v, err = %v", lib, err)
	}
}

func TestErrorsDoNotLeakKey(t *testing.T) {
	c := New("SECRETKEY", 0)
	c.APIBase = "http://127.0.0.1:1" // nothing listens here
	c.api = nil
	_, err := c.OwnedGames(context.Background(), "1")
	if err == nil || strings.Contains(err.Error(), "SECRETKEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestBudget(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"response":{"players":[{"steamid":"1"}]}}`)) })
	c.budget = NewBudget(1)
	if _, err := c.PlayerSummary(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PlayerSummary(context.Background(), "1"); err != ErrBudget {
		t.Fatalf("second call err = %v, want ErrBudget", err)
	}
}

func TestIsMultiplayer(t *testing.T) {
	bodies := map[string]string{
		"1": `{"1":{"success":true,"data":{"categories":[{"id":2,"description":"Single-player"},{"id":38,"description":"Online Co-op"}]}}}`,
		"2": `{"2":{"success":true,"data":{"categories":[{"id":2,"description":"Single-player"}]}}}`,
		"3": `{"3":{"success":true,"data":[]}}`,
		"4": `{"4":{"success":false}}`,
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(bodies[r.URL.Query().Get("appids")])) })
	want := map[string][2]bool{"1": {true, true}, "2": {false, true}, "3": {false, true}, "4": {false, false}}
	for id, w := range want {
		mp, known, err := c.IsMultiplayer(context.Background(), id)
		if err != nil || mp != w[0] || known != w[1] {
			t.Errorf("app %s: mp=%v known=%v err=%v, want %v", id, mp, known, err, w)
		}
	}
}

func openIDCallback(returnTo, steamID string) url.Values {
	q := url.Values{}
	q.Set("openid.ns", openIDNS)
	q.Set("openid.mode", "id_res")
	q.Set("openid.op_endpoint", steamOPEndpoint)
	q.Set("openid.claimed_id", "https://steamcommunity.com/openid/id/"+steamID)
	q.Set("openid.identity", "https://steamcommunity.com/openid/id/"+steamID)
	q.Set("openid.return_to", returnTo)
	q.Set("openid.response_nonce", "2026-11-22T10:00:00Zabc")
	q.Set("openid.assoc_handle", "1234567890")
	q.Set("openid.signed", "signed,op_endpoint,claimed_id,identity,return_to,response_nonce,assoc_handle")
	q.Set("openid.sig", "sig=")
	return q
}

func TestVerifyLogin(t *testing.T) {
	var gotMode string
	valid := true
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotMode = r.PostForm.Get("openid.mode")
		if valid {
			w.Write([]byte("ns:http://specs.openid.net/auth/2.0\nis_valid:true\n"))
		} else {
			w.Write([]byte("ns:http://specs.openid.net/auth/2.0\nis_valid:false\n"))
		}
	})
	returnTo := "https://bot.example/link/steam/callback?t=abc.def"
	id, err := c.VerifyLogin(context.Background(), openIDCallback(returnTo, "76561198000000001"), returnTo)
	if err != nil || id != "76561198000000001" {
		t.Fatalf("id = %q, err = %v", id, err)
	}
	if gotMode != "check_authentication" {
		t.Fatalf("mode sent to Steam = %q", gotMode)
	}

	if _, err := c.VerifyLogin(context.Background(), openIDCallback(returnTo, "76561198000000001"), "https://evil.example/cb"); err == nil {
		t.Fatal("mismatched return_to accepted")
	}
	bad := openIDCallback(returnTo, "76561198000000001")
	bad.Set("openid.claimed_id", "https://evil.example/openid/id/76561198000000001")
	if _, err := c.VerifyLogin(context.Background(), bad, returnTo); err == nil {
		t.Fatal("foreign claimed_id accepted")
	}
	valid = false
	if _, err := c.VerifyLogin(context.Background(), openIDCallback(returnTo, "76561198000000001"), returnTo); err == nil {
		t.Fatal("is_valid:false accepted")
	}
}

func TestLoginURL(t *testing.T) {
	c := New("k", 0)
	u, _ := url.Parse(c.LoginURL("https://bot.example/cb?t=x", "https://bot.example"))
	q := u.Query()
	if u.Host != "steamcommunity.com" || q.Get("openid.mode") != "checkid_setup" || q.Get("openid.return_to") != "https://bot.example/cb?t=x" {
		t.Fatalf("login url = %s", u)
	}
}
