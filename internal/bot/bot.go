// Package bot answers Discord interactions (slash commands, buttons and
// autocomplete) and runs the scheduled channel posts.
package bot

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"playradar/internal/discord"
	"playradar/internal/store"
)

// Syncer is the part of the background syncer the bot uses.
type Syncer interface {
	Sync(ctx context.Context, discordID, platform string) (store.Profile, int, error)
	SyncSoon(discordID, platform string)
	CheckGame(ctx context.Context, appID string) (*bool, error)
}

// Discord is the part of the REST client the bot uses.
type Discord interface {
	EditOriginal(ctx context.Context, interactionToken string, data discord.ResponseData) error
	CreateWebhook(ctx context.Context, channelID, name string) (discord.Webhook, error)
	DeleteWebhook(ctx context.Context, w discord.Webhook) error
	ExecuteWebhook(ctx context.Context, w discord.Webhook, msg discord.WebhookMessage) error
}

// Bot handles interactions. Fill every field before serving.
type Bot struct {
	AppName         string
	AvatarURL       string
	InstallURL      string
	Store           *store.Store
	Syncer          Syncer
	Discord         Discord
	PublicKey       ed25519.PublicKey
	LinkSecret      []byte
	Links           Links
	PlaytestsFile   string
	PlaytestsSource string
	CollectionsFile string // hand-picked game collections (data/collections.json)
	MomentsFile     string // rare gaming-moment posts (data/moments.json)
	Log             *slog.Logger
	Now             func() time.Time
	// EditDelay is the minimum wait before editing a deferred reply, so the
	// deferral reaches Discord first.
	EditDelay time.Duration

	wg sync.WaitGroup
}

// ServeHTTP is the interactions endpoint.
func (b *Bot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !discord.Verify(b.PublicKey, r, body) {
		http.Error(w, "invalid request signature", http.StatusUnauthorized)
		return
	}
	var in discord.Interaction
	if err := json.Unmarshal(body, &in); err != nil {
		b.log().Error("decode interaction", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	resp := b.Handle(r.Context(), &in)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		b.log().Error("write interaction response", "err", err)
	}
}

// Handle routes one interaction to its handler.
func (b *Bot) Handle(ctx context.Context, in *discord.Interaction) discord.Response {
	switch in.Type {
	case discord.InteractionPing:
		return discord.Response{Type: discord.ResponsePong}
	case discord.InteractionApplicationCommand:
		if err := b.Store.CountUsage(ctx, store.UsageCommand, in.Data.Name, in.GuildID, b.now()); err != nil {
			b.log().Warn("count command", "err", err)
		}
		return b.command(ctx, in)
	case discord.InteractionAutocomplete:
		return b.autocomplete(ctx, in)
	case discord.InteractionMessageComponent:
		return b.component(ctx, in)
	}
	return reply("That action isn't supported.", true)
}

func (b *Bot) command(ctx context.Context, in *discord.Interaction) discord.Response {
	switch in.Data.Name {
	case "link":
		return b.cmdLink(ctx, in)
	case "profile":
		return b.cmdProfile(ctx, in)
	case "playtests":
		return b.cmdPlaytests(ctx, in)
	case "whoplays":
		return b.cmdWhoPlays(ctx, in)
	case "incommon":
		return b.cmdInCommon(ctx, in)
	case "chart":
		return b.cmdChart(ctx, in)
	case "radar":
		return b.cmdRadar(ctx, in)
	case "visibility":
		return b.cmdVisibility(ctx, in)
	case "unlink":
		return b.cmdUnlink(ctx, in)
	case "game":
		return b.cmdGame(ctx, in)
	case "iceberg":
		return b.cmdIceberg(ctx, in)
	case "collage":
		return b.cmdCollage(ctx, in)
	case "crowns":
		return b.cmdCrowns(ctx, in)
	case "leaderboard":
		return b.cmdLeaderboard(ctx, in)
	case "taste":
		return b.cmdTaste(ctx, in)
	case "help":
		return b.cmdHelp(ctx, in)
	case "goty":
		return b.cmdGOTY(ctx, in)
	case "collection":
		return b.cmdCollection(ctx, in)
	case "verdict":
		return b.cmdVerdict(ctx, in)
	case "four":
		return b.cmdFour(ctx, in)
	}
	return reply("I don't know that command yet.", true)
}

// Wait blocks until deferred work has finished (used by tests and shutdown).
func (b *Bot) Wait() { b.wg.Wait() }

// later answers with a deferral now and edits in the real reply when work
// finishes. Use it for anything that might take more than a second or two.
func (b *Bot) later(in *discord.Interaction, ephemeral bool, work func(ctx context.Context) discord.ResponseData) discord.Response {
	token := in.Token
	start := time.Now()
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		data := work(ctx)
		if data.AllowedMentions == nil {
			data.AllowedMentions = discord.NoPings()
		}
		if wait := b.EditDelay - time.Since(start); wait > 0 {
			time.Sleep(wait)
		}
		if err := b.Discord.EditOriginal(ctx, token, data); err != nil {
			b.log().Error("edit deferred reply", "err", err)
		}
	}()
	resp := discord.Response{Type: discord.ResponseDeferredMessage}
	if ephemeral {
		resp.Data = &discord.ResponseData{Flags: discord.FlagEphemeral}
	}
	return resp
}

func (b *Bot) oops(err error, what string) discord.Response {
	b.log().Error(what, "err", err)
	return reply("Something went wrong on my side. Try again in a minute.", true)
}

func (b *Bot) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Bot) log() *slog.Logger {
	if b.Log != nil {
		return b.Log
	}
	return slog.Default()
}

// reply is a plain text answer.
func reply(text string, ephemeral bool) discord.Response {
	data := &discord.ResponseData{Content: text, AllowedMentions: discord.NoPings()}
	if ephemeral {
		data.Flags = discord.FlagEphemeral
	}
	return discord.Response{Type: discord.ResponseMessage, Data: data}
}

// message is a full message answer.
func message(data discord.ResponseData) discord.Response {
	if data.AllowedMentions == nil {
		data.AllowedMentions = discord.NoPings()
	}
	return discord.Response{Type: discord.ResponseMessage, Data: &data}
}

// update edits the message a button was clicked on. Whether it's ephemeral
// can't change, so that flag is dropped.
func update(data discord.ResponseData) discord.Response {
	if data.AllowedMentions == nil {
		data.AllowedMentions = discord.NoPings()
	}
	data.Flags &^= discord.FlagEphemeral
	return discord.Response{Type: discord.ResponseUpdateMessage, Data: &data}
}
