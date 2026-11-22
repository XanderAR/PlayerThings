package bot

import "playradar/internal/discord"

// Commands returns the slash command definitions to register with Discord.
func Commands() []discord.Command {
	everywhere := []int{discord.ContextGuild, discord.ContextBotDM, discord.ContextPrivateChannel}
	serversOnly := []int{discord.ContextGuild}
	anyInstall := []int{discord.IntegrationGuildInstall, discord.IntegrationUserInstall}
	serverInstall := []int{discord.IntegrationGuildInstall}
	manageServer := "32"
	textChannels := []int{discord.ChannelGuildText, discord.ChannelGuildAnnouncement}

	friend := func(name string, required bool) discord.CommandOption {
		return discord.CommandOption{Type: discord.OptionUser, Name: name, Description: "Someone to compare with", Required: required}
	}

	return []discord.Command{
		{
			Name: "link", Description: "Link your gaming accounts so your games follow you into every server",
			IntegrationTypes: anyInstall, Contexts: everywhere,
		},
		{
			Name: "profile", Description: "Show a gaming profile card",
			Options: []discord.CommandOption{
				{Type: discord.OptionUser, Name: "player", Description: "Whose profile (leave empty for yours)"},
			},
			IntegrationTypes: anyInstall, Contexts: everywhere,
		},
		{
			Name: "playtests", Description: "Live and upcoming playtests you can join on player.gg",
			Options: []discord.CommandOption{
				{Type: discord.OptionString, Name: "genre", Description: "Only show a genre, e.g. co-op or horror"},
			},
			IntegrationTypes: anyInstall, Contexts: everywhere,
		},
		{
			Name: "whoplays", Description: "See who in this server plays a game",
			Options: []discord.CommandOption{
				{Type: discord.OptionString, Name: "game", Description: "Start typing a game name", Required: true, Autocomplete: true},
			},
			IntegrationTypes: anyInstall, Contexts: serversOnly,
		},
		{
			Name: "incommon", Description: "Find multiplayer games you and your friends all own",
			Options: []discord.CommandOption{
				friend("friend", true), friend("friend2", false), friend("friend3", false), friend("friend4", false),
			},
			IntegrationTypes: anyInstall, Contexts: serversOnly,
		},
		{
			Name: "chart", Description: "This server's most-played games in the last two weeks",
			Options: []discord.CommandOption{
				{Type: discord.OptionChannel, Name: "weekly_in", Description: "Admins: also post the chart here every Monday", ChannelTypes: textChannels},
			},
			IntegrationTypes: anyInstall, Contexts: serversOnly,
		},
		{
			Name: "radar", Description: "Post new playtests that fit this server in a channel",
			Options: []discord.CommandOption{
				{Type: discord.OptionChannel, Name: "channel", Description: "Where to post (leave empty to see or stop the radar)", ChannelTypes: textChannels},
			},
			IntegrationTypes: serverInstall, Contexts: serversOnly, DefaultMemberPermissions: &manageServer,
		},
		{
			Name: "visibility", Description: "Show or hide yourself in this server's leaderboards",
			Options: []discord.CommandOption{
				{Type: discord.OptionString, Name: "setting", Description: "Show or hide", Required: true, Choices: []discord.Choice{
					{Name: "Show me in this server", Value: "show"},
					{Name: "Hide me in this server", Value: "hide"},
				}},
			},
			IntegrationTypes: anyInstall, Contexts: serversOnly,
		},
		{
			Name: "unlink", Description: "Delete everything this app stores about you",
			IntegrationTypes: anyInstall, Contexts: everywhere,
		},
	}
}
