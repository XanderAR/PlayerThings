package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"playradar/internal/discord"
	"playradar/internal/store"
)

// TestQA is the full click-through: every command, with every kind of option
// value, as a linked member, a hidden member and someone who never linked, in
// a server with data, an empty server and a DM. Then every button and menu
// option on every reply is clicked (by the person it was for, by another
// member and by an unlinked user where the message is public), following
// what each click opens. Payloads carry the fields Discord really sends,
// such as the numeric component id on clicks.
//
// Every answer must be a valid message (Components V2 rules), must not be an
// error ("Something went wrong", "That button has expired"), must not ping
// anyone, and deferred replies must be edited. Every button family the router
// knows must be reached at least once.
func TestQA(t *testing.T) {
	q := &qaRun{t: t, prefixes: map[string]int{}, commands: map[string]int{}}

	users := []string{"100", "500", "400", "999"} // linked and visible in G (500 has a backlog), linked but hidden in G, never linked
	for _, c := range Commands() {
		guildOnly := len(c.Contexts) == 1 && c.Contexts[0] == discord.ContextGuild
		perms := "0"
		if c.DefaultMemberPermissions != nil {
			perms = *c.DefaultMemberPermissions
		}
		for vi, opts := range qaVariants(c) {
			for _, user := range users {
				for _, place := range []string{"G", "H", ""} {
					if place == "" && guildOnly {
						continue
					}
					// Option variants other than the first run as the main user only.
					if vi > 0 && user != "100" {
						continue
					}
					h := qaHarness(t)
					label := qaLabel(c.Name, opts, user, place)
					q.commands[c.Name]++
					before := len(h.dc.edits)
					r, ok := q.send(h, label, qaCommand(c, place, user, perms, opts))
					if !ok {
						continue
					}
					seen := map[string]bool{}
					for _, m := range q.check(h, label, r, before, false) {
						q.crawl(h, label, place, user, m, 1, seen)
					}
				}
			}
		}
	}

	// Autocomplete on every option that has it.
	for _, c := range Commands() {
		for _, o := range c.Options {
			if !o.Autocomplete {
				continue
			}
			for _, place := range []string{"G", ""} {
				for _, typed := range []string{"", "de", "548430", "zzzz", "the"} {
					h := qaHarness(t)
					label := fmt.Sprintf("autocomplete /%s %s=%q in %s", c.Name, o.Name, typed, qaPlace(place))
					in := qaCommand(c, place, "100", "0", []map[string]any{{"name": o.Name, "type": o.Type, "value": typed, "focused": true}})
					in["type"] = discord.InteractionAutocomplete
					if r, ok := q.send(h, label, in); ok {
						q.check(h, label, r, 0, false)
					}
				}
			}
		}
	}

	// Scheduled posts: radar welcome, new playtests, the moment, the weekly
	// chart, and clicks on what they post.
	h := qaHarness(t)
	for _, in := range []map[string]any{
		qaCommand(findCommand("radar"), "G", "100", "32", []map[string]any{{"name": "channel", "type": discord.OptionChannel, "value": "C1"}}),
		qaCommand(findCommand("chart"), "G", "100", "32", []map[string]any{{"name": "weekly_in", "type": discord.OptionChannel, "value": "C2"}}),
	} {
		before := len(h.dc.edits)
		if r, ok := q.send(h, "feed setup", in); ok {
			q.check(h, "feed setup", r, before, true)
		}
	}
	// The radar status card, with its off button.
	if r, ok := q.send(h, "/radar status", qaCommand(findCommand("radar"), "G", "100", "32", nil)); ok {
		for _, m := range q.check(h, "/radar status", r, len(h.dc.edits), false) {
			q.crawl(h, "/radar status", "G", "100", m, 1, map[string]bool{})
		}
	}
	ctx := context.Background()
	h.st.MarkFeedPosted(ctx, "G", store.FeedWeeklyChart, now.AddDate(0, 0, -8))
	h.b.postNewPlaytests(ctx)
	h.b.postMoments(ctx)
	h.b.postDueCharts(ctx)
	if len(h.dc.posts) < 3 {
		q.fail("scheduled posts: got %d, want the welcome, a moment and a chart", len(h.dc.posts))
	}
	seen := map[string]bool{}
	for i, p := range h.dc.posts {
		label := fmt.Sprintf("scheduled post %d", i+1)
		d := postData(p)
		q.checkData(label, d)
		q.crawl(h, label, "G", "100", qaMsg{d, false}, 1, seen)
	}

	// Every button family must have been clicked.
	for _, p := range []string{"unlink", "vis", "radar", "pt", "lb", "game", "wp", "goty", "col", "mo", "vd", "four", "bl"} {
		if q.prefixes[p] == 0 {
			q.fail("no %q button or menu was ever reached", p)
		}
	}

	var fams []string
	for p, n := range q.prefixes {
		fams = append(fams, fmt.Sprintf("%s:%d", p, n))
	}
	sort.Strings(fams)
	t.Logf("QA: %d interactions (%d command runs over %d commands, %d clicks); clicks by family: %s",
		q.calls, sum(q.commands), len(q.commands), sumMap(q.prefixes), strings.Join(fams, " "))
	if len(q.failures) > 0 {
		sort.Strings(q.failures)
		t.Fatalf("%d problems:\n%s", len(q.failures), strings.Join(dedupe(q.failures), "\n"))
	}
}

type qaRun struct {
	t        *testing.T
	failures []string
	calls    int
	prefixes map[string]int
	commands map[string]int
}

// qaMsg is a reply to crawl, and whether only its owner can see it.
type qaMsg struct {
	data    discord.ResponseData
	private bool
}

func (q *qaRun) fail(format string, args ...any) {
	q.failures = append(q.failures, fmt.Sprintf(format, args...))
}

func qaHarness(t *testing.T) *harness {
	h := newHarness(t)
	h.seedBacklog(t)
	h.details(t, "548430", 300000, "Action")
	h.details(t, "730", 9000000, "Action", "Free To Play")
	h.details(t, "1145360", 60000, "Action", "Indie")
	h.details(t, "1966720", 400, "Indie")
	h.b.CollectionsFile = filepath.Join("..", "..", "data", "collections.json")
	h.b.MomentsFile = filepath.Join("..", "..", "data", "moments.json")
	if err := h.st.SetFavorites(context.Background(), "200", "steam", []string{"548430", "553850"}, now); err != nil {
		t.Fatal(err)
	}
	return h
}

func findCommand(name string) discord.Command {
	for _, c := range Commands() {
		if c.Name == name {
			return c
		}
	}
	panic("no command " + name)
}

// qaVariants lists option sets to try: required options only, then each
// optional value on its own.
func qaVariants(c discord.Command) [][]map[string]any {
	var required []map[string]any
	var extra [][]map[string]any
	for _, o := range c.Options {
		values := qaValues(c.Name, o)
		if o.Required {
			required = append(required, map[string]any{"name": o.Name, "type": o.Type, "value": values[0]})
			for _, v := range values[1:] {
				extra = append(extra, []map[string]any{{"name": o.Name, "type": o.Type, "value": v}})
			}
			continue
		}
		for _, v := range values {
			extra = append(extra, []map[string]any{{"name": o.Name, "type": o.Type, "value": v}})
		}
	}
	out := [][]map[string]any{required}
	for _, e := range extra {
		// Other required options stay set; a variant replaces one value.
		set := append([]map[string]any{}, e...)
		for _, r := range required {
			if r["name"] != e[0]["name"] {
				set = append(set, r)
			}
		}
		out = append(out, set)
	}
	if c.Name == "four" {
		out = append(out, []map[string]any{
			{"name": "first", "type": discord.OptionString, "value": "548430"},
			{"name": "second", "type": discord.OptionString, "value": "hades"},
			{"name": "third", "type": discord.OptionString, "value": "548430"},
		})
	}
	return out
}

func qaValues(command string, o discord.CommandOption) []any {
	switch {
	case o.Type == discord.OptionUser:
		return []any{"200", "400", "999", "100"}
	case o.Type == discord.OptionChannel:
		return []any{"C1"}
	case len(o.Choices) > 0:
		var out []any
		for _, c := range o.Choices {
			out = append(out, c.Value)
		}
		return out
	case command == "collection":
		return []any{"road-to-gta6", "coop-night", "no-such-collection"}
	case o.Autocomplete:
		return []any{"548430", "deep rock", "zzzz"}
	default:
		return []any{"co-op", "zzzz"}
	}
}

func qaUser(id string) map[string]any {
	return map[string]any{"id": id, "username": "user" + id, "global_name": nil, "avatar": nil, "discriminator": "0", "public_flags": 0}
}

// qaBase is an interaction with the envelope fields Discord sends today.
func qaBase(kind int, guild, user, perms string) map[string]any {
	in := map[string]any{
		"id": "1400000000000000000", "application_id": "1574183186173534369", "type": kind, "token": "itok", "version": 1,
		"locale": "en-US", "app_permissions": "2251799813685247", "entitlements": []any{}, "attachment_size_limit": 26214400,
		"authorizing_integration_owners": map[string]any{"1": user},
		"channel_id":                     "C0", "channel": map[string]any{"id": "C0", "type": 0},
	}
	if guild != "" {
		in["guild_id"], in["guild_locale"], in["context"] = guild, "en-US", 0
		in["guild"] = map[string]any{"id": guild, "locale": "en-US", "features": []any{}}
		in["member"] = map[string]any{"user": qaUser(user), "roles": []any{}, "joined_at": "2024-01-01T00:00:00.000000+00:00",
			"nick": nil, "avatar": nil, "permissions": perms, "flags": 0, "deaf": false, "mute": false}
	} else {
		in["context"] = 1
		in["user"] = qaUser(user)
	}
	return in
}

func qaCommand(c discord.Command, guild, user, perms string, opts []map[string]any) map[string]any {
	in := qaBase(discord.InteractionApplicationCommand, guild, user, perms)
	data := map[string]any{"id": "1574000000000000001", "name": c.Name, "type": 1}
	if len(opts) > 0 {
		data["options"] = opts
	}
	users := map[string]any{}
	for _, o := range opts {
		if o["type"] == discord.OptionUser {
			id := o["value"].(string)
			users[id] = qaUser(id)
		}
	}
	if len(users) > 0 {
		data["resolved"] = map[string]any{"users": users}
	}
	in["data"] = data
	return in
}

func qaClick(guild, user, customID string, compType int, values []string, msg discord.ResponseData) map[string]any {
	in := qaBase(discord.InteractionMessageComponent, guild, user, "0")
	data := map[string]any{"id": 7, "custom_id": customID, "component_type": compType}
	if compType == discord.ComponentStringSelect {
		data["values"] = values
	}
	in["data"] = data
	in["message"] = map[string]any{"id": "1400000000000000001", "channel_id": "C0", "type": 20, "flags": msg.Flags,
		"content": msg.Content, "components": msg.Components, "author": qaUser("1574183186173534369")}
	return in
}

func (q *qaRun) send(h *harness, label string, in map[string]any) (r rawResponse, ok bool) {
	defer func() {
		if p := recover(); p != nil {
			q.fail("%s: PANIC %v", label, p)
			ok = false
		}
	}()
	q.calls++
	r, code := h.send(q.t, in)
	if code != 200 {
		q.fail("%s: HTTP %d", label, code)
		return r, false
	}
	return r, true
}

// check validates one answer and returns the messages it produced.
func (q *qaRun) check(h *harness, label string, r rawResponse, editsBefore int, parentPrivate bool) []qaMsg {
	switch r.Type {
	case discord.ResponseMessage, discord.ResponseUpdateMessage:
		var d discord.ResponseData
		if err := json.Unmarshal(r.Data, &d); err != nil {
			q.fail("%s: bad data: %v", label, err)
			return nil
		}
		q.checkData(label, d)
		private := d.Flags&discord.FlagEphemeral != 0
		if r.Type == discord.ResponseUpdateMessage {
			private = parentPrivate
		}
		return []qaMsg{{d, private}}
	case discord.ResponseDeferredMessage:
		var d discord.ResponseData
		json.Unmarshal(r.Data, &d)
		h.b.Wait()
		h.dc.mu.Lock()
		edits := append([]discord.ResponseData(nil), h.dc.edits[editsBefore:]...)
		h.dc.mu.Unlock()
		if len(edits) == 0 {
			q.fail("%s: deferred reply was never filled in", label)
		}
		var out []qaMsg
		for _, e := range edits {
			q.checkData(label+" (deferred)", e)
			out = append(out, qaMsg{e, d.Flags&discord.FlagEphemeral != 0})
		}
		return out
	case discord.ResponseAutocomplete:
		var ac discord.AutocompleteData
		if err := json.Unmarshal(r.Data, &ac); err != nil || ac.Choices == nil {
			q.fail("%s: autocomplete must send a choices list: %s", label, r.Data)
			return nil
		}
		if len(ac.Choices) > 25 {
			q.fail("%s: %d choices (max 25)", label, len(ac.Choices))
		}
		for _, c := range ac.Choices {
			v := fmt.Sprint(c.Value)
			if c.Name == "" || len([]rune(c.Name)) > 100 || v == "" || len(v) > 100 {
				q.fail("%s: bad choice %+v", label, c)
			}
		}
	default:
		q.fail("%s: unexpected response type %d", label, r.Type)
	}
	return nil
}

var qaErrors = []string{"Something went wrong", "That button has expired", "I don't know that command", "isn't supported"}

func (q *qaRun) checkData(label string, d discord.ResponseData) {
	if d.Flags&discord.FlagComponentsV2 != 0 {
		if err := discord.CheckMessage(d); err != nil {
			q.fail("%s: invalid message: %v", label, err)
		}
	} else if strings.TrimSpace(d.Content) == "" && len(d.Embeds) == 0 && len(d.Components) == 0 {
		q.fail("%s: empty message", label)
	}
	if d.AllowedMentions == nil || len(d.AllowedMentions.Parse) > 0 {
		q.fail("%s: could ping people (allowed_mentions not locked down)", label)
	}
	all := d.Content + "\n" + texts(d)
	for _, bad := range qaErrors {
		if strings.Contains(all, bad) {
			first, _, _ := strings.Cut(strings.TrimSpace(all), "\n")
			q.fail("%s: error reply %q", label, first)
		}
	}
}

// crawl clicks every button and menu option on a message, then whatever
// those open, a few levels deep.
func (q *qaRun) crawl(h *harness, label, guild, owner string, m qaMsg, depth int, seen map[string]bool) {
	if depth > 4 {
		return
	}
	clickers := []string{owner}
	if !m.private {
		clickers = append(clickers, "200", "999")
	}
	var targets []discord.Component
	walk(m.data.Components, func(c discord.Component) {
		if (c.Type == discord.ComponentButton && c.CustomID != "") || c.Type == discord.ComponentStringSelect {
			targets = append(targets, c)
		}
	})
	for _, c := range targets {
		var picks [][]string
		if c.Type == discord.ComponentStringSelect {
			for i, o := range c.Options {
				if i < 4 {
					picks = append(picks, []string{o.Value})
				}
			}
			if c.MaxValues > 1 {
				var many []string
				for _, o := range c.Options[:c.MaxValues] {
					many = append(many, o.Value)
				}
				picks = append(picks, many)
			}
		} else {
			picks = [][]string{nil}
		}
		for _, user := range clickers {
			for _, vals := range picks {
				key := user + "|" + c.CustomID + "|" + strings.Join(vals, ",")
				if seen[key] {
					continue
				}
				seen[key] = true
				prefix, _, _ := strings.Cut(c.CustomID, ":")
				q.prefixes[prefix]++
				lbl := fmt.Sprintf("%s → click %s%s as %s", label, c.CustomID, qaVals(vals), user)
				before := len(h.dc.edits)
				r, ok := q.send(h, lbl, qaClick(guild, user, c.CustomID, c.Type, vals, m.data))
				if !ok {
					continue
				}
				for _, next := range q.check(h, lbl, r, before, m.private) {
					q.crawl(h, lbl, guild, user, next, depth+1, seen)
				}
			}
		}
	}
}

func qaVals(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return "=" + strings.Join(v, ",")
}

func qaPlace(p string) string {
	if p == "" {
		return "DM"
	}
	return "server " + p
}

func qaLabel(name string, opts []map[string]any, user, place string) string {
	var parts []string
	for _, o := range opts {
		parts = append(parts, fmt.Sprintf("%s:%v", o["name"], o["value"]))
	}
	return fmt.Sprintf("/%s %s (user %s, %s)", name, strings.Join(parts, " "), user, qaPlace(place))
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func sumMap(m map[string]int) int { return sum(m) }

func dedupe(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}
