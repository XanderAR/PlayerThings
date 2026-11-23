// Command playradar runs the Discord app: the interactions endpoint, the
// website, the library syncer and the scheduled posts.
//
//	playradar            serve (default)
//	playradar register   register slash commands (add -guild ID for instant testing)
//	playradar stats      print usage for the last 7 days
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"playradar/internal/bot"
	"playradar/internal/config"
	"playradar/internal/discord"
	"playradar/internal/steam"
	"playradar/internal/store"
	"playradar/internal/syncer"
	"playradar/internal/web"
)

func main() {
	if err := config.LoadDotEnv(".env"); err != nil {
		fail("read .env", err)
	}
	cfg, err := config.Load()
	if err != nil {
		fail("config", err)
	}
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		serve(cfg)
	case "register":
		register(cfg, os.Args[2:])
	case "stats":
		stats(cfg)
	case "preview":
		preview(cfg, os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "usage: playradar [serve | register [-guild ID] | stats | preview [-channel ID] [-you USER_ID]]")
		os.Exit(2)
	}
}

// preview renders every card from sample data and checks it against
// Discord's component rules. With -channel it also posts each card there as
// the bot, so Discord itself validates the layout and you can see it.
func preview(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("preview", flag.ExitOnError)
	channel := fs.String("channel", "", "post the cards in this channel (the bot needs to be able to send messages there)")
	you := fs.String("you", "100000000000000000", "Discord user ID to show as the person running the commands")
	only := fs.String("only", "", "comma-separated words; only cards whose name contains one of them")
	fs.Parse(args)

	dir, err := os.MkdirTemp("", "playradar-preview")
	if err != nil {
		fail("temp dir", err)
	}
	defer os.RemoveAll(dir)
	ctx := context.Background()
	cards, err := bot.Preview(ctx, dir, cfg.PlaytestsFile, cfg.BaseURL, *you)
	if err != nil {
		fail("render preview", err)
	}
	if *only != "" {
		var kept []bot.PreviewCard
		for _, c := range cards {
			for _, w := range strings.Split(*only, ",") {
				if w = strings.TrimSpace(w); w != "" && strings.Contains(strings.ToLower(c.Name), strings.ToLower(w)) {
					kept = append(kept, c)
					break
				}
			}
		}
		cards = kept
	}
	var rest *discord.Client
	if *channel != "" {
		if err := cfg.ValidateRegister(); err != nil {
			fail("config", err)
		}
		rest = discord.NewClient(cfg.DiscordAppID, cfg.DiscordBotToken)
		rest.UserAgent = fmt.Sprintf("DiscordBot (%s, 0.1)", cfg.BaseURL)
		intro := discord.ResponseData{Content: "**PlayRadar card preview** · sample players and hours; buttons point at sample data."}
		if err := rest.CreateMessage(ctx, *channel, intro); err != nil {
			fail("post preview", err)
		}
	}
	failed := 0
	for _, c := range cards {
		status := "ok"
		if err := discord.CheckMessage(c.Data); err != nil {
			status, failed = "INVALID: "+err.Error(), failed+1
		} else if rest != nil {
			labelled := c.Data
			labelled.Components = append([]discord.Component{discord.Text("-# Preview · " + c.Name)}, c.Data.Components...)
			if c.Data.Flags&discord.FlagComponentsV2 == 0 {
				labelled = c.Data
			}
			if err := rest.CreateMessage(ctx, *channel, labelled); err != nil {
				status, failed = "DISCORD REJECTED: "+err.Error(), failed+1
			}
			time.Sleep(700 * time.Millisecond) // stay well under the channel rate limit
		}
		fmt.Printf("%-28s %s\n", c.Name, status)
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func serve(cfg config.Config) {
	if err := cfg.ValidateServe(); err != nil {
		fail("config", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	st, err := store.Open(cfg.DatabasePath)
	if err != nil {
		fail("open database", err)
	}
	defer st.Close()

	pub, err := discord.ParsePublicKey(cfg.DiscordPublicKey)
	if err != nil {
		fail("DISCORD_PUBLIC_KEY", err)
	}
	if cfg.SteamAPIKey == "" {
		log.Warn("STEAM_API_KEY is not set: Steam sign-in works, but libraries won't sync until it is")
	}
	steamClient := steam.New(cfg.SteamAPIKey, cfg.SteamDailyBudget)
	sync := &syncer.Syncer{
		Store:     st,
		Providers: map[string]syncer.Provider{"steam": syncer.SteamProvider{Client: steamClient}},
		Checker:   steamClient,
		Interval:  cfg.SyncInterval,
		Log:       log,
	}
	rest := discord.NewClient(cfg.DiscordAppID, cfg.DiscordBotToken)
	rest.UserAgent = fmt.Sprintf("DiscordBot (%s, 0.1)", cfg.BaseURL)
	installURL := "https://discord.com/oauth2/authorize?client_id=" + cfg.DiscordAppID

	b := &bot.Bot{
		AppName:         cfg.AppName,
		AvatarURL:       cfg.AvatarURL,
		InstallURL:      installURL,
		Store:           st,
		Syncer:          sync,
		Discord:         rest,
		PublicKey:       pub,
		LinkSecret:      []byte(cfg.LinkSecret),
		Links:           bot.Links{Site: cfg.BaseURL, PlayerGG: cfg.PlayerGGURL},
		PlaytestsFile:   cfg.PlaytestsFile,
		PlaytestsSource: cfg.PlaytestsSource,
		Log:             log,
		EditDelay:       500 * time.Millisecond,
	}
	site := &web.Server{
		AppName:      cfg.AppName,
		BaseURL:      cfg.BaseURL,
		Contact:      cfg.Contact,
		InstallURL:   installURL,
		PlayerGGURL:  cfg.PlayerGGURL,
		Store:        st,
		Steam:        steamClient,
		Syncer:       sync,
		LinkSecret:   []byte(cfg.LinkSecret),
		Interactions: b,
		Log:          log,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go sync.Run(ctx)
	go b.RunFeeds(ctx)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           site.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Info("listening", "port", cfg.Port, "base_url", cfg.BaseURL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fail("http server", err)
	}
	b.Wait()
}

func register(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	guild := fs.String("guild", "", "register to one server only (updates instantly; for testing)")
	_ = fs.Parse(args)
	if err := cfg.ValidateRegister(); err != nil {
		fail("config", err)
	}
	rest := discord.NewClient(cfg.DiscordAppID, cfg.DiscordBotToken)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := rest.RegisterCommands(ctx, *guild, bot.Commands()); err != nil {
		fail("register commands", err)
	}
	if *guild != "" {
		fmt.Printf("Registered %d commands to server %s.\n", len(bot.Commands()), *guild)
		return
	}
	fmt.Printf("Registered %d global commands. They can take a few minutes to appear.\n", len(bot.Commands()))
}

func stats(cfg config.Config) {
	st, err := store.Open(cfg.DatabasePath)
	if err != nil {
		fail("open database", err)
	}
	defer st.Close()
	ctx := context.Background()
	totals, err := st.CountTotals(ctx)
	if err != nil {
		fail("totals", err)
	}
	since := time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02")
	rows, err := st.UsageSince(ctx, since)
	if err != nil {
		fail("usage", err)
	}
	fmt.Printf("Linked users: %d\nServers with opted-in members: %d\nScheduled feeds: %d\n\n", totals.LinkedUsers, totals.VisibleServers, totals.Feeds)
	fmt.Printf("Since %s:\n", since)
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tNAME\tCOUNT\tSERVERS")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\n", r.Kind, r.Name, r.Count, r.Servers)
	}
	tw.Flush()
}

func fail(what string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
	os.Exit(1)
}
