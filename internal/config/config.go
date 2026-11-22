// Package config loads settings from environment variables (and an optional .env file).
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config holds every setting the service reads at startup.
type Config struct {
	AppName          string
	BaseURL          string // public https URL, no trailing slash
	Port             string
	DiscordAppID     string
	DiscordPublicKey string // hex, from the Developer Portal "General Information" page
	DiscordBotToken  string // only needed to register slash commands
	SteamAPIKey      string
	LinkSecret       string // signs account-link URLs; 32+ random characters
	DatabasePath     string
	PlaytestsFile    string
	PlaytestsSource  string // shown in the /playtests footer
	Contact          string // shown on the privacy page
	SyncInterval     time.Duration
	SteamDailyBudget int
	PlayerGGURL      string // where every "on player.gg" button points
	AvatarURL        string // optional avatar for scheduled posts
}

// Load reads the configuration from the environment, applying defaults.
func Load() (Config, error) {
	c := Config{
		AppName:          env("APP_NAME", "PlayRadar"),
		BaseURL:          strings.TrimRight(env("BASE_URL", "http://localhost:8080"), "/"),
		Port:             env("PORT", "8080"),
		DiscordAppID:     env("DISCORD_APP_ID", ""),
		DiscordPublicKey: env("DISCORD_PUBLIC_KEY", ""),
		DiscordBotToken:  env("DISCORD_BOT_TOKEN", ""),
		SteamAPIKey:      env("STEAM_API_KEY", ""),
		LinkSecret:       env("LINK_SECRET", ""),
		DatabasePath:     env("DATABASE_PATH", "data/playradar.db"),
		PlaytestsFile:    env("PLAYTESTS_FILE", "data/playtests.json"),
		PlaytestsSource:  env("PLAYTESTS_SOURCE", "public playtest pages"),
		Contact:          env("CONTACT", ""),
		PlayerGGURL:      strings.TrimRight(env("PLAYERGG_URL", "https://player.gg"), "/"),
		AvatarURL:        env("AVATAR_URL", ""),
	}

	interval, err := time.ParseDuration(env("SYNC_INTERVAL", "12h"))
	if err != nil {
		return c, fmt.Errorf("SYNC_INTERVAL: %w", err)
	}
	c.SyncInterval = interval

	budget, err := strconv.Atoi(env("STEAM_DAILY_BUDGET", "90000"))
	if err != nil {
		return c, fmt.Errorf("STEAM_DAILY_BUDGET: %w", err)
	}
	c.SteamDailyBudget = budget
	return c, nil
}

// ValidateServe reports settings that must be present to serve traffic.
func (c Config) ValidateServe() error {
	missing := missingKeys(map[string]string{
		"DISCORD_APP_ID":     c.DiscordAppID,
		"DISCORD_PUBLIC_KEY": c.DiscordPublicKey,
		"LINK_SECRET":        c.LinkSecret,
	})
	if len(missing) > 0 {
		return fmt.Errorf("missing settings: %s", strings.Join(missing, ", "))
	}
	if len(c.LinkSecret) < 32 {
		return errors.New("LINK_SECRET must be at least 32 characters")
	}
	return nil
}

// ValidateRegister reports settings needed to register slash commands.
func (c Config) ValidateRegister() error {
	missing := missingKeys(map[string]string{
		"DISCORD_APP_ID":    c.DiscordAppID,
		"DISCORD_BOT_TOKEN": c.DiscordBotToken,
	})
	if len(missing) > 0 {
		return fmt.Errorf("missing settings: %s", strings.Join(missing, ", "))
	}
	return nil
}

// LoadDotEnv reads KEY=VALUE lines from path into the process environment
// without overriding variables that are already set. A missing file is fine.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		val = strings.TrimSpace(val)
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, val); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func missingKeys(values map[string]string) []string {
	var missing []string
	for k, v := range values {
		if v == "" {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return missing
}
