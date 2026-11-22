package discord

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"type":1}`)
	ts := "1700000000"
	sig := hex.EncodeToString(ed25519.Sign(priv, append([]byte(ts), body...)))

	r := httptest.NewRequest("POST", "/interactions", nil)
	r.Header.Set("X-Signature-Ed25519", sig)
	r.Header.Set("X-Signature-Timestamp", ts)
	if !Verify(pub, r, body) {
		t.Fatal("valid signature rejected")
	}
	if Verify(pub, r, []byte(`{"type":2}`)) {
		t.Fatal("signature accepted for a different body")
	}
	r.Header.Set("X-Signature-Ed25519", "zz")
	if Verify(pub, r, body) {
		t.Fatal("malformed signature accepted")
	}
}

func TestParsePublicKey(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	got, err := ParsePublicKey(hex.EncodeToString(pub))
	if err != nil || !got.Equal(pub) {
		t.Fatalf("got %x, err %v", got, err)
	}
	if _, err := ParsePublicKey("abcd"); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestClearComponentsMarshalsEmptyList(t *testing.T) {
	raw, _ := json.Marshal(ResponseData{Content: "done", ClearComponents: true})
	if !strings.Contains(string(raw), `"components":[]`) {
		t.Fatalf("got %s", raw)
	}
	raw, _ = json.Marshal(ResponseData{Content: "hi"})
	if strings.Contains(string(raw), "components") {
		t.Fatalf("components should be omitted: %s", raw)
	}
}

func TestOptionValues(t *testing.T) {
	var in Interaction
	_ = json.Unmarshal([]byte(`{"data":{"options":[{"name":"game","type":3,"value":"Deep Rock","focused":true},{"name":"n","type":4,"value":5}]}}`), &in)
	if got := in.Data.StringOption("game"); got != "Deep Rock" {
		t.Fatalf("game = %q", got)
	}
	if got := in.Data.StringOption("n"); got != "5" {
		t.Fatalf("n = %q", got)
	}
	if f, ok := in.Data.Focused(); !ok || f.Name != "game" {
		t.Fatalf("focused = %+v", f)
	}
}

func TestCanManageServer(t *testing.T) {
	cases := map[string]bool{"0": false, "32": true, "8": true, "1024": false, "": false}
	for perms, want := range cases {
		m := &Member{Permissions: perms}
		if got := m.CanManageServer(); got != want {
			t.Errorf("perms %q: got %v want %v", perms, got, want)
		}
	}
	var nilMember *Member
	if nilMember.CanManageServer() {
		t.Error("nil member can manage")
	}
}
