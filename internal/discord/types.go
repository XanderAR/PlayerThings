// Package discord is a thin layer over Discord's HTTP interactions API:
// request types, response types, signature checks and the two REST calls the
// bot needs. The bot never opens a gateway connection.
package discord

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Interaction types.
const (
	InteractionPing               = 1
	InteractionApplicationCommand = 2
	InteractionMessageComponent   = 3
	InteractionAutocomplete       = 4
	InteractionModalSubmit        = 5
)

// Interaction response types.
const (
	ResponsePong            = 1
	ResponseMessage         = 4
	ResponseDeferredMessage = 5
	ResponseDeferredUpdate  = 6
	ResponseUpdateMessage   = 7
	ResponseAutocomplete    = 8
)

// Message flags.
const (
	// FlagEphemeral makes a reply visible only to the person who ran the command.
	FlagEphemeral = 1 << 6
	// FlagComponentsV2 lays the message out entirely with components
	// (containers, sections, text, galleries). Content and embeds must be empty.
	FlagComponentsV2 = 1 << 15
)

// Where a command can be used, and how the app can be installed.
const (
	ContextGuild          = 0
	ContextBotDM          = 1
	ContextPrivateChannel = 2

	IntegrationGuildInstall = 0
	IntegrationUserInstall  = 1
)

// Command option types.
const (
	OptionString  = 3
	OptionInteger = 4
	OptionBoolean = 5
	OptionUser    = 6
	OptionChannel = 7

	ChannelGuildText         = 0
	ChannelGuildAnnouncement = 5
)

// Component types and button styles.
const (
	ComponentActionRow    = 1
	ComponentButton       = 2
	ComponentStringSelect = 3
	ComponentSection      = 9
	ComponentTextDisplay  = 10
	ComponentThumbnail    = 11
	ComponentMediaGallery = 12
	ComponentSeparator    = 14
	ComponentContainer    = 17

	SpacingSmall = 1
	SpacingLarge = 2

	ButtonPrimary   = 1
	ButtonSecondary = 2
	ButtonSuccess   = 3
	ButtonDanger    = 4
	ButtonLink      = 5
)

// User is a Discord user.
type User struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
	Avatar     string `json:"avatar"`
}

// DisplayName returns the name Discord shows for u.
func (u User) DisplayName() string {
	if u.GlobalName != "" {
		return u.GlobalName
	}
	return u.Username
}

// Member is a user's membership in a server.
type Member struct {
	User        *User  `json:"user"`
	Nick        string `json:"nick"`
	Permissions string `json:"permissions"` // computed permissions in the channel
}

// Permission bits the bot checks.
const (
	PermAdministrator = 1 << 3
	PermManageGuild   = 1 << 5
)

// CanManageServer reports whether the member has Manage Server or Administrator.
func (m *Member) CanManageServer() bool {
	if m == nil {
		return false
	}
	var p uint64
	if _, err := fmt.Sscan(m.Permissions, &p); err != nil {
		return false
	}
	return p&PermAdministrator != 0 || p&PermManageGuild != 0
}

// Interaction is the payload Discord POSTs to the interactions endpoint.
type Interaction struct {
	ID            string          `json:"id"`
	ApplicationID string          `json:"application_id"`
	Type          int             `json:"type"`
	Token         string          `json:"token"`
	GuildID       string          `json:"guild_id"`
	ChannelID     string          `json:"channel_id"`
	Member        *Member         `json:"member"`
	User          *User           `json:"user"`
	Data          InteractionData `json:"data"`
}

// InteractionData holds command or component details.
type InteractionData struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Options       []Option  `json:"options"`
	Resolved      *Resolved `json:"resolved"`
	CustomID      string    `json:"custom_id"`
	ComponentType int       `json:"component_type"`
	Values        []string  `json:"values"` // chosen select menu options
}

// Resolved carries full objects for users mentioned in options.
type Resolved struct {
	Users   map[string]User   `json:"users"`
	Members map[string]Member `json:"members"`
}

// Option is one command option as sent by Discord.
type Option struct {
	Name    string          `json:"name"`
	Type    int             `json:"type"`
	Value   json.RawMessage `json:"value"`
	Focused bool            `json:"focused"`
	Options []Option        `json:"options"`
}

// Invoker returns the user who triggered the interaction.
func (i *Interaction) Invoker() User {
	if i.Member != nil && i.Member.User != nil {
		return *i.Member.User
	}
	if i.User != nil {
		return *i.User
	}
	return User{}
}

// InGuild reports whether the interaction happened in a server.
func (i *Interaction) InGuild() bool { return i.GuildID != "" }

// Option returns the top-level option called name.
func (d InteractionData) Option(name string) (Option, bool) {
	for _, o := range d.Options {
		if o.Name == name {
			return o, true
		}
	}
	return Option{}, false
}

// StringOption returns the option's value as a string ("" if absent).
func (d InteractionData) StringOption(name string) string {
	o, ok := d.Option(name)
	if !ok {
		return ""
	}
	return o.StringValue()
}

// Focused returns the option the user is typing in during autocomplete.
func (d InteractionData) Focused() (Option, bool) {
	for _, o := range d.Options {
		if o.Focused {
			return o, true
		}
	}
	return Option{}, false
}

// ResolvedUser looks up a user mentioned in an option.
func (d InteractionData) ResolvedUser(id string) (User, bool) {
	if d.Resolved == nil {
		return User{}, false
	}
	u, ok := d.Resolved.Users[id]
	return u, ok
}

// StringValue decodes a string value, falling back to the raw JSON for numbers.
func (o Option) StringValue() string {
	var s string
	if err := json.Unmarshal(o.Value, &s); err == nil {
		return s
	}
	return strings.Trim(string(o.Value), `"`)
}

// Response is what the endpoint returns to Discord. Data is a *ResponseData
// for messages or an AutocompleteData for autocomplete results.
type Response struct {
	Type int `json:"type"`
	Data any `json:"data,omitempty"`
}

// AutocompleteData is the body of an autocomplete response.
type AutocompleteData struct {
	Choices []Choice `json:"choices"`
}

// ResponseData is a message body.
type ResponseData struct {
	Content         string           `json:"content,omitempty"`
	Embeds          []Embed          `json:"embeds,omitempty"`
	Components      []Component      `json:"components,omitempty"`
	Flags           int              `json:"flags,omitempty"`
	AllowedMentions *AllowedMentions `json:"allowed_mentions,omitempty"`

	// ClearComponents sends an explicit empty component list, which removes
	// buttons when a message is updated.
	ClearComponents bool `json:"-"`
}

// MarshalJSON lets ClearComponents force "components": [].
func (d ResponseData) MarshalJSON() ([]byte, error) {
	type alias ResponseData
	if d.ClearComponents && len(d.Components) == 0 {
		return json.Marshal(struct {
			alias
			Components []Component `json:"components"`
		}{alias(d), []Component{}})
	}
	return json.Marshal(alias(d))
}

// AllowedMentions controls who a message may ping.
type AllowedMentions struct {
	Parse []string `json:"parse"`
}

// NoPings is an AllowedMentions value that pings nobody.
func NoPings() *AllowedMentions { return &AllowedMentions{Parse: []string{}} }

// Embed is a rich message card.
type Embed struct {
	Title       string       `json:"title,omitempty"`
	URL         string       `json:"url,omitempty"`
	Description string       `json:"description,omitempty"`
	Color       int          `json:"color,omitempty"`
	Fields      []EmbedField `json:"fields,omitempty"`
	Thumbnail   *EmbedImage  `json:"thumbnail,omitempty"`
	Image       *EmbedImage  `json:"image,omitempty"`
	Footer      *EmbedFooter `json:"footer,omitempty"`
	Author      *EmbedAuthor `json:"author,omitempty"`
}

// EmbedField is a titled block inside an embed.
type EmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

// EmbedImage is an image or thumbnail.
type EmbedImage struct {
	URL string `json:"url"`
}

// EmbedFooter is the small text under an embed.
type EmbedFooter struct {
	Text    string `json:"text"`
	IconURL string `json:"icon_url,omitempty"`
}

// EmbedAuthor is the small line above an embed title.
type EmbedAuthor struct {
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	IconURL string `json:"icon_url,omitempty"`
}

// Component is any message component. Which fields apply depends on Type.
type Component struct {
	Type       int         `json:"type"`
	Components []Component `json:"components,omitempty"` // action row, section, container
	Style      int         `json:"style,omitempty"`
	Label      string      `json:"label,omitempty"`
	CustomID   string      `json:"custom_id,omitempty"`
	URL        string      `json:"url,omitempty"`
	Disabled   bool        `json:"disabled,omitempty"`

	Content     string         `json:"content,omitempty"`      // text display (markdown)
	Accessory   *Component     `json:"accessory,omitempty"`    // section: thumbnail or button
	Media       *Media         `json:"media,omitempty"`        // thumbnail
	Description string         `json:"description,omitempty"`  // thumbnail alt text
	Items       []MediaItem    `json:"items,omitempty"`        // media gallery
	Divider     *bool          `json:"divider,omitempty"`      // separator
	Spacing     int            `json:"spacing,omitempty"`      // separator
	AccentColor *int           `json:"accent_color,omitempty"` // container
	Options     []SelectOption `json:"options,omitempty"`      // string select
	Placeholder string         `json:"placeholder,omitempty"`  // string select
	MinValues   *int           `json:"min_values,omitempty"`   // string select
	MaxValues   int            `json:"max_values,omitempty"`   // string select
}

// Media points at an image by URL.
type Media struct {
	URL string `json:"url"`
}

// MediaItem is one image in a media gallery.
type MediaItem struct {
	Media       Media  `json:"media"`
	Description string `json:"description,omitempty"`
}

// SelectOption is one choice in a select menu.
type SelectOption struct {
	Label       string `json:"label"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default,omitempty"` // pre-selected
}

// ActionRow groups up to five buttons, or one select menu.
func ActionRow(buttons ...Component) Component {
	return Component{Type: ComponentActionRow, Components: buttons}
}

// Container is a card with an optional accent bar (color < 0 for none).
func Container(accent int, children ...Component) Component {
	c := Component{Type: ComponentContainer, Components: children}
	if accent >= 0 {
		c.AccentColor = &accent
	}
	return c
}

// Text is a block of markdown.
func Text(markdown string) Component {
	return Component{Type: ComponentTextDisplay, Content: markdown}
}

// Section shows 1-3 blocks of text beside an accessory (a Thumbnail or a button).
func Section(accessory Component, texts ...string) Component {
	c := Component{Type: ComponentSection, Accessory: &accessory}
	for _, t := range texts {
		c.Components = append(c.Components, Text(t))
	}
	return c
}

// Thumbnail is a small image, used as a section accessory.
func Thumbnail(url, alt string) Component {
	return Component{Type: ComponentThumbnail, Media: &Media{URL: url}, Description: truncate(alt, 1024)}
}

// Gallery shows 1-10 images in a grid.
func Gallery(items ...MediaItem) Component {
	return Component{Type: ComponentMediaGallery, Items: items}
}

// Separator adds space between components, with or without a visible line.
func Separator(line bool, spacing int) Component {
	return Component{Type: ComponentSeparator, Divider: &line, Spacing: spacing}
}

// Select is a string select menu.
func Select(customID, placeholder string, options ...SelectOption) Component {
	return Component{Type: ComponentStringSelect, CustomID: customID, Placeholder: truncate(placeholder, 150), Options: options}
}

// LinkButton opens url in the browser.
func LinkButton(label, url string) Component {
	return Component{Type: ComponentButton, Style: ButtonLink, Label: truncate(label, 80), URL: url}
}

// Button sends a component interaction with customID when clicked.
func Button(style int, label, customID string) Component {
	return Component{Type: ComponentButton, Style: style, Label: truncate(label, 80), CustomID: customID}
}

// Choice is an autocomplete or fixed option choice.
type Choice struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Command is a slash command definition for registration.
type Command struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	Type             int             `json:"type,omitempty"`
	Options          []CommandOption `json:"options,omitempty"`
	IntegrationTypes []int           `json:"integration_types,omitempty"`
	Contexts         []int           `json:"contexts,omitempty"`
	// DefaultMemberPermissions limits who sees the command by default
	// (a permission bitfield as a string, e.g. "32" for Manage Server).
	DefaultMemberPermissions *string `json:"default_member_permissions,omitempty"`
}

// CommandOption is one option of a slash command definition.
type CommandOption struct {
	Type         int      `json:"type"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Required     bool     `json:"required,omitempty"`
	Autocomplete bool     `json:"autocomplete,omitempty"`
	Choices      []Choice `json:"choices,omitempty"`
	ChannelTypes []int    `json:"channel_types,omitempty"`
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Truncate shortens s to at most n characters, adding an ellipsis.
func Truncate(s string, n int) string { return truncate(s, n) }
