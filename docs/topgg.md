# top.gg listing

Everything to paste into top.gg's "Add a bot" form. Images are served by the
app at `/media/` (built with `go run ./tools/topgg`, see that file for how).

## Form fields

| Field | Value |
|---|---|
| Bot ID | `1574183186173534369` |
| Prefix | `/` (slash commands) |
| Invite link | `https://discord.com/oauth2/authorize?client_id=1574183186173534369` |
| Website | `https://playradar-production.up.railway.app` |
| Support server | none yet (top.gg doesn't require one) |
| Tags | Game, Social, Fun, Utility, Leveling (pick the closest five the form offers; add "steam" if custom tags are allowed) |
| Background / banner | `https://playradar-production.up.railway.app/media/banner.png` (1600x600) |

## Short description (156 of 200 characters)

```
Steam stats for your server: profile cards, Four Favorites, roasts, crowns, a pile-of-shame picker and player.gg playtests. Unofficial, built for player.gg.
```

## Long description (Markdown)

```markdown
![Player.gg for Discord](https://playradar-production.up.railway.app/media/banner.png)

**Who here actually plays Elden Ring? Who has the most hours in Deep Rock? What should we play tonight?**

Player.gg turns everyone's Steam library into something your server can talk about: profile cards, Four Favorites, a court that roasts your playtime, crowns for the most hours in a game, and playtests you can join before release.

**Free** · **Slash commands only** · **Works in servers, DMs and as a user app** · **No Steam password, ever**

---

## ✈️ Get started in 30 seconds

1. `/link` and sign in on Steam's own site. Player.gg only receives your Steam ID.
2. `/profile` to see your card.
3. `/four` to pick the four games that made you, then post it.

Your Steam **Game details** privacy setting needs to be **Public** for your library to load.

---

## ⚖️ /verdict: the court will see you now

An archetype and three lines of evidence, pulled from your real stats. The accused can **Appeal** for the case for the defense, and anyone watching can press **Judge me** to be next.

![The /verdict card](https://playradar-production.up.railway.app/media/card-verdict.png)

## ❤️ /four: Four Favorites

The four games that made you, Letterboxd style. In a server, the card shows who else here has the same favorites and who your closest match is.

![Four favorites](https://playradar-production.up.railway.app/media/shelf-four.jpg)

## 👤 /profile and /collage

Hours played, most played, what you're playing lately, and your top games as one cover grid.

![The /profile card](https://playradar-production.up.railway.app/media/card-profile.png)

![A /collage grid](https://playradar-production.up.railway.app/media/shelf-collage.jpg)

## 📚 /backlog: your pile of shame

How much of your library you've never launched, the best-known games sitting in it, and a 🎲 pick for tonight that you can reroll.

![The /backlog card](https://playradar-production.up.railway.app/media/card-backlog.png)

## 👑 Server competition

- `/whoplays` everyone here who owns a game, ranked by hours
- `/crowns` games where you have the most hours in the server. Play more to take someone's crown
- `/leaderboard` hours, last 2 weeks, games played, crowns and pile of shame
- `/taste` how your library and favorite genres compare with a friend's
- `/incommon` multiplayer games your whole group owns
- `/chart` the server's most played games this fortnight, optionally posted every Monday

## 🏆 /goty and /collection

Your Game of the Year race from this year's releases, with a button to vote on player.gg. Collections show how much of a list you've played, with what's left dimmed.

![The /goty card](https://playradar-production.up.railway.app/media/card-goty.png)

![A collection board](https://playradar-production.up.railway.app/media/shelf-collection.jpg)

## 🧪 Playtests from player.gg

`/playtests` lists live and upcoming playtests you can join. Admins can run `/radar` to post new ones in a channel, with a note when a playtest fits what people in the server play.

![The /playtests card](https://playradar-production.up.railway.app/media/card-playtests.png)

---

## 📋 All commands

**You:** `/link` `/profile` `/four` `/verdict` `/backlog` `/iceberg` `/collage` `/goty` `/collection` `/game`
**This server:** `/whoplays` `/leaderboard` `/crowns` `/taste` `/incommon` `/chart`
**Playtests:** `/playtests` `/radar`
**Privacy and help:** `/visibility` `/unlink` `/help`

## 🔒 Privacy

- Steam sign-in happens on Steam's own site. Player.gg never sees your password.
- It reads your public Steam profile, owned games and playtime, about twice a day.
- Slash commands only: it can't read your messages, and its replies never ping anyone.
- `/visibility` hides you in any server, and `/unlink` deletes everything stored about you.
- Server installs only ask for **Manage Webhooks**, used by `/radar` and the weekly chart.

## ❓ FAQ

**Is it free?**
Yes, every feature.

**My library shows nothing.**
In Steam, go to Profile → Edit Profile → Privacy Settings and set **Game details** to **Public**. It syncs again within a few minutes.

**Can I use it without adding it to a server?**
Yes. Add it as a user app and use it anywhere, including DMs.

**Is this an official player.gg bot?**
Not yet. It's an unofficial proof of concept built for player.gg, using public Steam data and links to player.gg pages. It isn't affiliated with Valve or Steam.

## 🔗 Links

- [Add Player.gg to Discord](https://discord.com/oauth2/authorize?client_id=1574183186173534369)
- [Create your player.gg account](https://player.gg/join)
- [Privacy](https://playradar-production.up.railway.app/privacy) · [Terms](https://playradar-production.up.railway.app/terms)
```

## How this was shaped

Patterns from popular listings in the same space (November 2026):

- **.fmbot** (1.06M servers, music stats, the model for this bot): one-line pitch, "connect an account to get started", emoji-headed feature and social sections, links, and a support note that it's not affiliated with Last.fm.
- **Soova** (1.87K votes, Steam game nights): opens with a question, one section per feature with an example of the output, a privacy and safety section, "setup takes a minute" steps and an FAQ. Uses a background banner.
- **FreeStuff** (268K servers, free games): "easy to set up", a getting-started checklist, keyword tags and a screenshot of what its posts look like.
