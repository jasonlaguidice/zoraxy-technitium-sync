# zoraxy-technitium-sync

A Zoraxy plugin that keeps [Technitium DNS Server](https://technitium.com/dns/)
A/AAAA records in sync with Zoraxy's configured HTTP reverse-proxy host
rules. It's a small, spiritual replacement for one feature of the
discontinued [Mantrae](https://github.com/mizuchilabs/mantrae) project, which
used to manage Traefik + Technitium together — this does the same job for
Zoraxy + Technitium.

Every 30 seconds (configurable) it polls Zoraxy's own local plugin API for
the list of enabled HTTP proxy host rules, and makes sure each hostname (plus
any configured aliases) has an A record in Technitium pointing at your
Zoraxy box's LAN IP. AAAA records are an optional global on/off toggle
pointing at one shared IPv6 target. Records it creates are marked with an
ownership TXT record so it never touches DNS entries it didn't create itself,
and removes records automatically when a proxy host rule is deleted or
disabled.

## How it works

- **Zoraxy side**: polls `GET /plugin/api/proxy/list?type=host` on Zoraxy's
  local plugin API (there is no change-notification webhook for proxy host
  rules in the current Zoraxy plugin SDK, so polling is the only option).
- **Technitium side**: uses Technitium's HTTP API (token auth) to add,
  update and delete A/AAAA/TXT records.
- **Ownership tracking**: before touching any A/AAAA record for a hostname,
  the plugin manages a TXT record at `_ztsync.<hostname>` with value
  `heritage=zoraxy-technitium-sync,instance=<instanceID>`. It will only
  update or delete a record if that exact marker is present and matches its
  own instance ID. Records that exist without the marker are left alone,
  logged at info level, and never touched.
- **Restart recovery**: ownership is never cached only in memory — every
  reconcile cycle (including the first one after a restart) re-derives which
  hostnames it owns directly from the TXT markers already in the zone.

## Installing on a Zoraxy host

Zoraxy plugins are plain binaries, not archives or containers: the binary's
filename must match its containing folder's name. There are three ways to
get this plugin onto a Zoraxy host.

### 1. Custom Plugin Store source

This repository publishes its own self-hosted Plugin Store index via GitHub
Pages, updated automatically on every release. In Zoraxy's admin UI, go to
**Settings -> Plugin Store -> Add Source** and paste:

```
https://jasonlaguidice.github.io/zoraxy-technitium-sync/index.json
```

Zoraxy will then list "Technitium Sync" in its Plugin Store and handle
downloading and updating the right binary for your host.

Enable "Technitium Sync" from Zoraxy's plugin list.

### 2. Manual install

Download the release asset matching your host's OS/architecture from the
[latest release](https://github.com/jasonlaguidice/zoraxy-technitium-sync/releases/latest)
— e.g. `zoraxy-technitium-sync_linux_amd64`,
`zoraxy-technitium-sync_linux_arm64`, or
`zoraxy-technitium-sync_windows_amd64.exe` — and place it at:

```
<zoraxy data dir>/plugins/zoraxy-technitium-sync/zoraxy-technitium-sync
```

Then make it executable and restart Zoraxy (or use its plugin manager UI to
reload plugins):

```sh
chmod +x <zoraxy data dir>/plugins/zoraxy-technitium-sync/zoraxy-technitium-sync
```

Enable "Technitium Sync" from Zoraxy's plugin list.

## Configuring

Open the plugin's page from Zoraxy's web admin (Plugins list). The settings
panel covers:

| Field | Default | Notes |
|---|---|---|
| Technitium base URL | *(empty)* | Required — e.g. `http://192.168.1.254:5380` |
| Technitium API token | *(empty)* | Generate one in Technitium's admin UI |
| DNS zone | *(empty)* | Required |
| Record TTL | `300` seconds |  |
| Poll interval | `30` seconds | How often Zoraxy's proxy list is checked |
| LAN IPv4 target | auto-detected | The outbound-facing LAN IP of the box the plugin is running on, detected at first run; override in the UI if it picks the wrong interface |
| AAAA enabled | off | Global toggle, applies to every managed host |
| LAN IPv6 target | auto-detected | Same auto-detection as LAN IPv4, at first run; falls back to blank if none is found. Only required if AAAA is enabled |
| Instance ID | generated once | Used in the ownership TXT marker; read-only |

The status panel on the same page shows the last poll time, last error (if
any), how many records are currently managed, and the hosts created,
updated, deleted or skipped on the most recent cycle.

## Building

Requires Go 1.25+.

The Zoraxy plugin SDK (`imuslab.com/zoraxy/mod/plugins/zoraxy_plugin`) is
brought in as a git submodule at `third_party/zoraxy` — it isn't a
standalone module, it's a subdirectory of the full
[tobychui/zoraxy](https://github.com/tobychui/zoraxy) application repo, so
that whole repo is checked out (pinned to `v3.3.4`) and wired in via a
`replace` directive in `go.mod`. Clone with submodules:

```sh
git clone --recurse-submodules https://github.com/jasonlaguidice/zoraxy-technitium-sync.git
```

or, if you already have a plain clone:

```sh
git submodule update --init
```

```sh
go build -o zoraxy-technitium-sync .
```

Run the unit tests:

```sh
go test . ./internal/...
```

Verify the plugin's self-description (used by Zoraxy to discover it, no
running Zoraxy instance required):

```sh
./zoraxy-technitium-sync -introspect
```

## License

Copyright (C) 2026 Jason LaGuidice

**AGPL-3.0-or-later**

The Zoraxy plugin SDK this depends on lives in `third_party/zoraxy/` and is AGPL under the same terms. See [LICENSE](LICENSE) and
[NOTICE](NOTICE).

The license text also travels inside the binary. Release assets are bare
binaries — Zoraxy's registry indexer builds direct download URLs, so nothing
can be wrapped in an archive carrying a LICENSE beside it — and AGPL §4,
reached through §6, requires a copy of the License to reach whoever receives
the object code. LICENSE and NOTICE are therefore embedded and served by the
running plugin:

| Path | Serves |
| --- | --- |
| `/plugin.ui/com.github.jasonlaguidice.zoraxy-technitium-sync/license` | the AGPL-3.0 text |
| `/plugin.ui/com.github.jasonlaguidice.zoraxy-technitium-sync/notice` | NOTICE |