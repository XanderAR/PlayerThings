package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client makes the few REST calls the bot needs.
type Client struct {
	BaseURL  string // https://discord.com/api/v10
	AppID    string
	BotToken string
	HTTP     *http.Client
	// UserAgent must look like "DiscordBot (url, version)".
	UserAgent string
}

// NewClient returns a client for the given application.
func NewClient(appID, botToken string) *Client {
	return &Client{
		BaseURL:  "https://discord.com/api/v10",
		AppID:    appID,
		BotToken: botToken,
		HTTP:     &http.Client{Timeout: 15 * time.Second},
	}
}

// APIError is an error response from Discord.
type APIError struct {
	Status  int
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("discord API %d (code %d): %s", e.Status, e.Code, e.Message)
}

// Discord error codes the bot reacts to.
const (
	CodeUnknownWebhook     = 10015
	CodeMissingAccess      = 50001
	CodeMissingPermissions = 50013
)

// HasCode reports whether err is a Discord API error with the given code.
func HasCode(err error, code int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

// Webhook identifies a channel webhook the app created.
type Webhook struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

// WebhookMessage is a message posted through a webhook.
type WebhookMessage struct {
	Username        string           `json:"username,omitempty"`
	AvatarURL       string           `json:"avatar_url,omitempty"`
	Content         string           `json:"content,omitempty"`
	Embeds          []Embed          `json:"embeds,omitempty"`
	Components      []Component      `json:"components,omitempty"`
	Flags           int              `json:"flags,omitempty"`
	AllowedMentions *AllowedMentions `json:"allowed_mentions,omitempty"`
}

// RegisterCommands overwrites the app's slash commands. With a guildID they
// are registered to that one server, which updates instantly (good for testing).
func (c *Client) RegisterCommands(ctx context.Context, guildID string, cmds []Command) error {
	path := fmt.Sprintf("/applications/%s/commands", c.AppID)
	if guildID != "" {
		path = fmt.Sprintf("/applications/%s/guilds/%s/commands", c.AppID, guildID)
		// Install types and contexts only apply to global commands.
		local := make([]Command, len(cmds))
		for i, cmd := range cmds {
			cmd.IntegrationTypes = nil
			cmd.Contexts = nil
			local[i] = cmd
		}
		cmds = local
	}
	return c.do(ctx, http.MethodPut, path, true, cmds, nil)
}

// EditOriginal replaces a deferred reply with its final content. Interaction
// tokens authorize this call, so no bot token is sent.
func (c *Client) EditOriginal(ctx context.Context, interactionToken string, data ResponseData) error {
	path := fmt.Sprintf("/webhooks/%s/%s/messages/@original", c.AppID, interactionToken)
	// Ephemerality is fixed by the deferred response; only the layout flag can be set here.
	data.Flags &= FlagComponentsV2
	return c.do(ctx, http.MethodPatch, path, false, data, nil)
}

// CreateWebhook makes a webhook in a channel so the app can post there on a
// schedule without holding a gateway connection. Needs Manage Webhooks.
func (c *Client) CreateWebhook(ctx context.Context, channelID, name string) (Webhook, error) {
	var w Webhook
	err := c.do(ctx, http.MethodPost, "/channels/"+channelID+"/webhooks", true, map[string]string{"name": name}, &w)
	return w, err
}

// DeleteWebhook removes a webhook the app created. The webhook token
// authorizes the call.
func (c *Client) DeleteWebhook(ctx context.Context, w Webhook) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		fmt.Sprintf("%s/webhooks/%s/%s", c.BaseURL, w.ID, w.Token), nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return &APIError{Status: resp.StatusCode, Message: resp.Status}
	}
	return nil
}

// CreateMessage posts a message in a channel as the bot. Only the layout
// flag is kept (bot messages can't be ephemeral).
func (c *Client) CreateMessage(ctx context.Context, channelID string, data ResponseData) error {
	data.Flags &= FlagComponentsV2
	if data.AllowedMentions == nil {
		data.AllowedMentions = NoPings()
	}
	return c.do(ctx, http.MethodPost, "/channels/"+channelID+"/messages", true, data, nil)
}

// ExecuteWebhook posts a message through a webhook the app created.
func (c *Client) ExecuteWebhook(ctx context.Context, w Webhook, msg WebhookMessage) error {
	path := fmt.Sprintf("/webhooks/%s/%s?wait=true&with_components=true", w.ID, w.Token)
	return c.do(ctx, http.MethodPost, path, false, msg, nil)
}

func (c *Client) do(ctx context.Context, method, path string, auth bool, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	ua := c.UserAgent
	if ua == "" {
		ua = "DiscordBot (https://localhost, 0.1)"
	}
	req.Header.Set("User-Agent", ua)
	if auth {
		req.Header.Set("Authorization", "Bot "+c.BotToken)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode}
		if json.Unmarshal(raw, apiErr) != nil || apiErr.Message == "" {
			apiErr.Message = string(raw)
		} else if resp.StatusCode == http.StatusBadRequest {
			// Keep Discord's field-by-field details: they say which component is wrong.
			apiErr.Message += " " + Truncate(string(raw), 1500)
		}
		return apiErr
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}
