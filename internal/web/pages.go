package web

const pageTemplates = `
{{define "layout"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.App}}</title>
<style>
:root { --bg:#f6f6f8; --card:#fff; --text:#17171c; --muted:#5d5f6b; --line:#e3e3ea; --accent:#6d4aff; --warn:#9a5b00; --warn-bg:#fdf0dc; }
@media (prefers-color-scheme: dark) { :root { --bg:#0f0f13; --card:#18181f; --text:#ececf1; --muted:#a3a5b3; --line:#2a2a35; --accent:#9d86ff; --warn:#f0b45a; --warn-bg:#33260f; } }
* { box-sizing:border-box; }
body { margin:0; background:var(--bg); color:var(--text); font:16px/1.6 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif; }
main { max-width:640px; margin:0 auto; padding:56px 16px 64px; }
.card { background:var(--card); border:1px solid var(--line); border-radius:16px; padding:28px 24px; }
h1 { font-size:28px; line-height:1.2; margin:0 0 12px; letter-spacing:-.01em; }
h2 { font-size:18px; margin:28px 0 8px; }
p, li { color:var(--muted); }
ul { padding-left:20px; }
a { color:var(--accent); }
.btn { display:inline-block; background:var(--accent); color:#fff; text-decoration:none; font-weight:600; padding:12px 20px; border-radius:10px; margin-top:8px; }
.btn-alt { background:transparent; color:var(--accent); border:1px solid var(--line); }
.note { background:var(--warn-bg); color:var(--warn); border-radius:10px; padding:12px 14px; font-size:15px; }
footer { margin-top:24px; font-size:14px; color:var(--muted); text-align:center; }
footer a { color:var(--muted); }
</style>
</head>
<body>
<main>
<div class="card">
{{if eq .Page "home"}}{{template "home" .}}{{end}}
{{if eq .Page "link"}}{{template "link" .}}{{end}}
{{if eq .Page "result"}}{{template "result" .}}{{end}}
{{if eq .Page "privacy"}}{{template "privacy" .}}{{end}}
{{if eq .Page "terms"}}{{template "terms" .}}{{end}}
</div>
<footer><a href="/">{{.App}}</a> · <a href="/privacy">Privacy</a> · <a href="/terms">Terms</a></footer>
</main>
</body>
</html>{{end}}

{{define "home"}}
<h1>{{.App}}</h1>
<p>Playtests, gaming profiles and "who plays what" for your Discord server. Every card links to <a href="{{.PlayerGG}}">player.gg</a>, where you can join playtests and keep a shelf of your games.</p>
{{if .InstallURL}}<a class="btn" href="{{.InstallURL}}">Add to Discord</a>{{end}}
{{end}}

{{define "link"}}
<h1>Link your Steam account</h1>
{{if .For}}<p>This link is for the Discord account <b>{{.For}}</b>. If that isn't you, close this page: someone may be trying to link your Steam to their account.</p>{{end}}
{{if .Discord}}<p><b>Fastest:</b> if Steam is connected to your Discord account, approve a Discord sign-in and we'll use that Steam account. We only read your connections to find it, and the access ends straight away.</p>
<a class="btn" href="/link/discord?t={{.Token}}">Use the Steam account on my Discord</a>
<p style="margin-top:20px">Or sign in on Steam's own site. {{.App}} only receives your Steam ID, never your password.</p>
<a class="btn btn-alt" href="{{.SteamURL}}">Sign in through Steam</a>
{{else}}<p>You'll sign in on Steam's own site. {{.App}} only receives your Steam ID, never your password.</p>
<a class="btn" href="{{.SteamURL}}">Sign in through Steam</a>{{end}}
<p style="margin-top:20px">We then read your public Steam profile, the games you own and your playtime, and refresh them about twice a day.</p>
{{if .InServer}}<p>Because you started from a server, you'll also appear on that server's leaderboards. Turn that off any time with <b>/visibility</b>.</p>{{end}}
<p style="margin-top:20px;font-size:14px">Your Steam "Game details" privacy setting must be Public for your library to show. <b>/unlink</b> deletes everything.</p>
{{end}}

{{define "result"}}
<h1>{{.Title}}</h1>
{{if .Body}}<p>{{.Body}}</p>{{end}}
{{if .Private}}
<p class="note">Steam is hiding your library. In Steam, open <b>Profile → Edit Profile → Privacy Settings</b> and set <b>Game details</b> to Public. We'll pick it up within a few hours.</p>
{{end}}
{{if .ActionURL}}<a class="btn" href="{{.ActionURL}}">{{.ActionLabel}}</a>{{end}}
{{if .Done}}<p>You can close this tab. While you're here, <a href="{{.PlayerGG}}/join">start your player.gg shelf</a>.</p>{{end}}
{{end}}

{{define "privacy"}}
<h1>Privacy</h1>
<p>This Discord app is an unofficial proof of concept built for player.gg. It isn't yet an official player.gg product, and it isn't operated by Valve or Discord.</p>
<h2>What we store</h2>
<ul>
<li>Your Discord user ID.</li>
<li>Your Steam ID, Steam display name, avatar and profile link.</li>
<li>If you link through Discord, we read your Discord connections once to find your verified Steam account. We keep only the Steam ID and end the access straight away.</li>
<li>The Steam games you own, your playtime and when you last played, refreshed about twice a day, plus daily playtime snapshots.</li>
<li>Which servers you've chosen to appear in.</li>
<li>For server admins who turn on scheduled posts: the channel and the webhook used to post there.</li>
<li>Daily counts of commands run and links clicked per server. These don't include who ran or clicked them.</li>
</ul>
<h2>How it's used</h2>
<p>Only to run the commands: your profile card, "who plays" lists, shared games and server charts. Other people only see your data in servers where you've opted in. We don't sell it, share it with advertisers or data brokers, or use it to train AI.</p>
<p>Links to player.gg pass through our site so we can count clicks; player.gg then receives standard tags saying the visit came from Discord.</p>
<h2>Your choices</h2>
<ul>
<li><b>/visibility hide</b> hides you in one server.</li>
<li><b>/unlink</b> deletes everything about you immediately.</li>
<li>Making your Steam game details private stops us from seeing your library.</li>
</ul>
{{if .Contact}}<h2>Contact</h2><p>{{.Contact}}</p>{{end}}
{{end}}

{{define "terms"}}
<h1>Terms</h1>
<p>This Discord app is a free, unofficial proof of concept provided as is, without guarantees that it will be available or error-free.</p>
<p>Don't use it to harass people, scrape data, or get around anyone's privacy settings. We may stop serving any server or user that does.</p>
<p>Game data comes from Steam and is shown under Valve's API terms. {{.App}} isn't endorsed by Valve, Discord or player.gg.</p>
<p>See the <a href="/privacy">privacy page</a> for what's stored and how to delete it.</p>
{{end}}
`
