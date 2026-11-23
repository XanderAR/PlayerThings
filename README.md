# PlayRadar (working name)

A Discord app that gives players another way to experience player.gg and sends them there. It has playtests, profile cards and "who plays what" for servers, and every card links to player.gg. The product spec with mock output for every command is in [docs/spec.html](docs/spec.html).

This is an unofficial proof of concept built for player.gg. It uses only public Steam data and public player.gg URLs, and isn't yet an official player.gg product.

## What works

| Command | What it does |
|---|---|
| `/help` | Every command, grouped |
| `/link` | Links Steam through Steam's own sign-in. Linking from a server opts you into that server's leaderboards |
| `/profile [player]` | Profile card: hours, library size, most played, playing lately, top covers |
| `/game <game>` | One game: your hours, who here owns it, the top player, multiplayer, release year, genres, Steam reviews, iceberg tier |
| `/iceberg [player]` | Your 40 most-played games sorted into tiers by Steam review count, with a depth score |
| `/four [player] [first..fourth]` | Four favorites: four covers with your hours, and who in the server shares them. With no games given, a menu of your most played |
| `/verdict [player]` | A roast from real stats: an archetype, three lines of evidence and a playtest "sentence". The accused can appeal; anyone can press Judge me |
| `/backlog [player]` | Pile of shame: the share of your Steam library never launched, the best-known unplayed games as covers, your rank in the server, and a rerollable pick for tonight |
| `/collage [player] [period]` | Your top 9 games as a cover grid, all time or the last 2 weeks |
| `/whoplays <game>` | Opted-in members ranked by hours, with a crown for the top player. Game names autocomplete |
| `/leaderboard [type]` | Server ranking by hours, last 2 weeks, games played or crowns, with tabs to switch |
| `/crowns [player]` | Games where you have the most hours in this server, and who's next |
| `/taste @friend` | Taste match %: shared games, hours side by side, genre split |
| `/incommon @friends` | Games everyone owns, multiplayer first (Steam store tags) |
| `/chart [weekly_in]` | Server's most played in the last 2 weeks. Admins can have it posted every Monday |
| `/playtests [genre]` | Paged playtest list with "Request access" buttons and a details view |
| `/radar [channel]` | Admins: posts new playtests in a channel, noting how many members play similar games |
| `/visibility show/hide` | Show or hide yourself in one server |
| `/unlink` | Deletes everything stored about you |

Replies use Discord's Components V2 layout (cards, sections, galleries). `go run ./cmd/playradar preview` renders every card from sample data and checks it against Discord's component limits; add `-channel ID` to post them in a channel so Discord validates them too.

Every game button opens the game's page on player.gg. `https://player.gg/explore/games/<steam app id>.steam` resolves to player.gg's own page for that game. All player.gg links pass through `/go`, which counts the click and adds `utm_source=playradar`.

## How it's built

- **Go, one binary, one dependency** (`modernc.org/sqlite`, pure Go).
- **HTTP interactions only.** Discord POSTs slash commands to `/interactions`. There's no gateway connection and no privileged intents (presence, members, message content), so the bot never reaches Discord's 10,000-user review for that data.
- **Scheduled posts** (`/radar`, weekly `/chart`) go through a webhook the bot creates in the chosen channel. That needs the **Manage Webhooks** permission.
- **SQLite** holds accounts, libraries and settings. Tables are keyed by platform, so Xbox or PlayStation can be added as another `syncer.Provider` next to Steam.
- **Steam** calls are paced to 1 per second, which keeps them under Valve's 100,000 a day. Libraries refresh every 12 hours.

```
cmd/playradar      entry point: serve, register, stats
internal/bot       commands, buttons, autocomplete, scheduled posts
internal/web       link pages, /go redirect, privacy and terms
internal/steam     Web API, store tags, OpenID sign-in
internal/syncer    background library sync
internal/store     SQLite
internal/discord   interaction types, signature check, REST calls
```

## Setup

You need to do these steps yourself, because they create accounts and keys.

1. **Discord app.** In the [Developer Portal](https://discord.com/developers/applications), create an application.
   - Don't put "Discord" in its name.
   - Copy the **Application ID** and **Public Key**.
   - Under **Bot**, reset the token. It's only needed to register commands.
   - Under **Installation**, enable **User Install** with `applications.commands`.
   - Also enable **Guild Install** with `applications.commands` and `bot`, and the **Manage Webhooks** permission.
2. **Steam key.** Get one at <https://steamcommunity.com/dev/apikey>, using your service's domain. The service starts without it and Steam sign-in still works, but libraries only sync once it's set.
3. **Deploy.** On Railway, create a service from this folder's repo; the Dockerfile is picked up automatically.
   - Add a volume mounted at `/data`.
   - Generate a domain and set it as `BASE_URL`.
   - Set the variables from `.env.example`.
4. **Point Discord at it.** In the portal:
   - Set **Interactions Endpoint URL** to `BASE_URL/interactions`. Discord sends a signed test ping, so the service must already be running with the right public key.
   - Set the **Privacy Policy URL** to `BASE_URL/privacy`.
   - Set the **Terms of Service URL** to `BASE_URL/terms`.
5. **Register commands.** Do this locally with a `.env` that has `DISCORD_APP_ID` and `DISCORD_BOT_TOKEN`.

   Register to one test server first, which updates instantly:

   ```powershell
   go run ./cmd/playradar register -guild YOUR_TEST_SERVER_ID
   ```

   Then register globally, which takes a few minutes to appear:

   ```powershell
   go run ./cmd/playradar register
   ```

6. **Add playtests.** `data/playtests.json` holds the list. It was filled on 2026-11-23 from the playtests with dates on <https://player.gg/playtests?upcoming=1>, so refresh it by hand as tests end.
   - `url` is the game's Playtest tab on player.gg (`.../explore/games/<slug>/playtest`).
   - `similar_steam_apps` drives the "N members here play games like X" line.
   - Write summaries in your own words, one short line each.
   - `go test ./...` checks the file, and it's built into the image, so pushing it redeploys.

Once the bot is in more than 100 servers, Discord requires app verification, which includes an identity check.

## Measuring it

Commands run and clicks to player.gg are counted per day and per server, without user IDs. To see the last 7 days:

```powershell
go run ./cmd/playradar stats
```

That reads the local database. For the deployed one, run `/app/playradar stats` inside the container via `railway ssh`.

## Tests

```powershell
go test ./...
```

The tests cover:
- signature checks
- Steam parsing and OpenID verification
- every command's output
- per-server privacy
- the radar and weekly posts
- the link flow
- the `/go` allowlist

The race detector wasn't run, because it needs cgo, which isn't set up on this machine.

## Not done yet

- Not yet tried against live Discord and Steam. That needs your app and keys.
- Image cards. Replies are embeds for now.
- player.gg accounts, shelves, reviews and quests. These need player.gg data.
- PlayStation and Xbox linking.
- `/review`, `/curator` and `/goty` from the spec.
- Playtests are a hand-curated file, not a live feed.
