# Player.gg in Discord: why servers want it, and how it feeds player.gg

Research notes behind this: Letterboxd, Backloggd, Steam Replay, Spotify Wrapped and Receiptify mechanics, fun-bot loops (fmbot, Pokétwo, Mudae, the Wordle Activity), Discord's developer policy, and the gaming calendar to mid-2027. Sources are listed at the end.

## Why a server adds it

1. **Bragging rights that only exist inside your server.** fmbot's WhoKnows and crowns prove the loop: someone holds the crown for a game, someone else plays more to take it, everyone sees it. We have `/whoplays`, `/crowns`, `/leaderboard`.
2. **It answers real questions.** "What can the four of us play tonight?" (`/incommon`), "who here plays Deep Rock?" (`/whoplays`), "is this playtest our kind of thing?" (radar fit line).
3. **It's a reason to talk.** Identity cards people post and argue about: `/iceberg`, `/taste`, `/collage`, `/goty`. Spotify's "Listening Age" got around 116K mentions in a week, mostly because it was wrong in funny ways.
4. **It shows up for the big moments**, then goes quiet. GTA VI week, The Game Awards, the Winter Sale.
5. **Members need no setup.** It's user-installable, works in DMs, and replies are visible to people who don't have the app. That makes every public reply an ad for the bot.

## The funnel: every card ends on player.gg

| Feature | Hook | player.gg destination |
|---|---|---|
| `/goty` (built) | "Your 2026 GOTY race", in GOTY season | player.gg/goty (live since Nov 18) |
| `/collection` (built) | "You've played 3 of 8 GOTY winners" | player.gg/collections: their collection pages show no progress today, and the bot adds it |
| Moments (built) | One-tap tally for GTA VI or TGA | GOTY vote, a game page |
| `/playtests`, `/radar` (built) | "3 members here play games like this" | Playtest tab: request access |
| `/profile`, `/collage`, `/iceberg` (built) | Identity cards | "Build your shelf on player.gg" |
| `/game`, `/whoplays` (built) | Game card | The game's player.gg page |

Every link goes through `/go`, which tags `utm_source=playradar&utm_campaign=<command>&utm_content=<server>` and counts clicks per command and per server (`playradar stats`).

## Moments: rare, opt-in, interactive

Discord's developer policy bans unsolicited DMs, ads aimed at users, and automated messages sent to keep a server active. So moments are:
- Sent only to the channel an admin picked with `/radar`.
- Posted once per server, and capped at about 6–8 a year.
- Questions people answer with one tap (a live tally), never ads.

Each one is a JSON entry in `data/moments.json`, so adding a moment takes no code.

Live now: **GTA VI launch** (Nov 19 – Dec 3; console-only, so the tally replaces Steam data) and **The Game Awards** (Dec 7–10).

Calendar for the next six months (dates are verified unless marked):

| When | Moment | Card |
|---|---|---|
| Nov 26–30 | Steam Black Friday | "What are you grabbing?" tally plus the server's most-owned unplayed games (pile of shame) |
| Dec 10–11 | TGA results | "Did your GOTY win?": the server's GOTY race against the winner, linking to player.gg/goty results |
| mid-Dec (Steam Replay date unconfirmed) | Year in review | the bot's own `/replay` card, released before Steam's |
| Dec 17 – Jan 4 | Steam Winter Sale | "Pile of shame": how many owned games nobody here has launched |
| Feb 8–15 | Couch Co-Op Fest | The co-op collection, with who here is furthest along |
| Feb 22 – Mar 1 | Next Fest | Demo bracket built from playtests |
| Feb 23 | Fable launch | "Rate it day one", linking to a player.gg review |
| Apr 8 | Final Fantasy VII Revelation launch | "Rate it day one", linking to a player.gg review |
| May 20 | God of War Laufey launch | "Rate it day one", linking to a player.gg review |
| Jun 14–21 | Next Fest | Demo bracket |
| Jun 24 | Summer Sale | Pile of shame |

## Next gimmicks, ranked by likely spread per hour of work

1. **Gaming age** (XS). "Your gaming age is 2011": the release year of your games, weighted by hours. One provocative number, the Spotify Listening Age format. The data is already stored.
2. **`/four` Four Favorites** (S). Letterboxd's most-copied format. Pick four games and the card shows them as a cover strip on your profile; `/taste` can weigh them; "post your four" threads. The renderer already draws it.
3. **`/verdict`** (S). An fmbot `.judge`-style roast: an archetype ("The Factory Foreman") plus three lines pulled from real stats ("62% of your playtime is Factorio. The factory must grow."). No AI needed.
4. **`/backlog` pile of shame** (S). The share of your library you've never launched, plus a "🎲 Pick one for me" button that rerolls.
5. **`/receipt`** (S-M). The Receiptify parody: your top 10 games as line items, hours as prices, with "CASHIER: player.gg" at the bottom. It needs a new image template.
6. **Daily guess-the-game** (M). A zoomed crop of a cover with four buttons, server streaks, and yesterday's summary posted to the radar channel. This is the Wordle Activity loop.
7. **9:16 story images** (M). A "Share" version of `/goty`, `/iceberg` and `/four` for Instagram and TikTok. player.gg's GOTY page already makes story images, so match its look.
8. **Needs player.gg access (next week)**:
   - **GOTY voter roles:** the bot gives a "GOTY '26 voter" Discord role after someone votes on player.gg. Needs a webhook from player.gg.
   - **One-liner `/review`:** writes a short review to player.gg, and the most-reacted review of the week is featured.
   - **Server Showdowns:** a monthly prompt where members submit a top 5 and the result is published as a player.gg collection credited to the server.
   - **Linking player.gg accounts:** shelves, XP and quests in cards.

## What the cards look like (Components V2)

- **One container per card** with an accent bar: purple for you, gold for GOTY and crowns, green for live playtests, blue for friends, orange for moments.
- **A title, then a small grey subtitle,** then a divider and the body.
- **Medals 🥇🥈🥉 for the top three,** numbers after that.
- **Images are drawn by the bot** (`/render/shelf.jpg`), with uncropped covers and ranks or hours in a strip underneath. Discord's own galleries crop to squares.
- **Buttons sit under the card:** the player.gg link first, then app actions (tabs, Back, answer buttons).
- **Every layout is checked against Discord's limits in tests.** `go run ./cmd/playradar preview -channel ID` posts every card to a channel for a visual check.

## Sources

- Letterboxd and its peers:
  - https://player.gg/goty
  - https://player.gg/collections
  - https://player.gg/changelog
  - https://thespinoff.co.nz/culture/24-04-2025/how-letterboxds-four-favourites-took-over-the-internet
  - https://www.ypulse.com/newsfeed/2024/03/06/letterboxd-successfully-used-virality-to-gain-a-huge-loyal-gen-z-userbase/
  - https://backloggd.com/about/recent-news/
  - https://techcrunch.com/2025/12/04/spotify-says-wrapped-2025-is-its-biggest-yet-with-200m-users-in-its-first-day
  - https://www.meltwater.com/en/blog/spotify-wrapped-listening-age-analysis
  - https://heavy.com/news/2020/09/receiptify-spotify
- Discord:
  - https://support-dev.discord.com/hc/en-us/articles/8563934450327-Discord-Developer-Policy
  - https://docs.discord.com/developers/tutorials/developing-a-user-installable-app
  - https://docs.discord.com/developers/topics/rate-limits
  - https://support-apps.discord.com/hc/en-us/articles/31598005086359-Wordle-FAQ
  - https://fmbot-discord.github.io/docs/guildsettings/crownsettings/
- Dates:
  - https://www.forbes.com/sites/erikkain/2026/11/16/gta-6-release-date-time-ps5-xbox-series-x-download-size-day-one-update/
  - https://thegameawards.com/news/tga-returns-december-10-2026
  - https://partner.steamgames.com/doc/marketing/upcoming_events
  - https://www.gematsu.com/2026/06/fable-launches-february-23-2027
  - https://www.videogameschronicle.com/news/square-enix-confirms-final-fantasy-7-revelation-release-date/
  - https://www.gematsu.com/2026/11/god-of-war-laufey-delayed-to-may-20-2027
