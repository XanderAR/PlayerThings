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
	default:
		fmt.Fprintln(os.Stderr, "usage: playradar [serve | register [-guild ID] | stats]")
		os.Exit(2)
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
