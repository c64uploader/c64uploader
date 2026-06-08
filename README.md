# C64 Ultimate Uploader

Remotely upload and run programs on Commodore 64 Ultimate via its [REST API](https://1541u-documentation.readthedocs.io/en/latest/api/api_calls.html).

Three ways to browse your Assembly64 collection, all backed by the same Go server and SQLite index:

- **a64browser cart on the C64 itself.** A native EasyFlash cartridge image (`a64browser.crt`) that runs on the C64 Ultimate. Set it as the default cart in firmware config and the C64 boots straight into the browser on power-on — search, drill into Games/Demos/Music/…, hit Return to load and run. **F7** cleanly drops back to BASIC. No PC interaction needed once the server is running on your network.
- **Mobile web UI on your phone.** Open `http://<server>:8000/` in a phone browser for the same hierarchy as the cart, with thumb-friendly tap targets and instant search. Tap an entry → it launches on the C64.
- **Stock firmware Assembly64 browser.** Point the Ultimate's built-in browser at your local server (Spiffy-compatible HTTP API) and it works against your offline mirror instead of `assembly64.com`.

> [!NOTE]
> This code was generated with AI coding assistant and is not thoroughly tested or reviewed.

![System Diagram](./docs/system-diagram.png)

## Demo

![Demo](screenshots/c64uploader.gif)

The clip above is sped up 8× for the README. Full 96-second demo with audio: [screenshots/c64uploader.mp4](screenshots/c64uploader.mp4).

## Features

This project consists of two components:

### c64uploader (Go application)

A command-line tool that runs on your PC and communicates with the C64 Ultimate via its REST API. It provides multiple modes of operation:

- **TUI Mode** - Interactive terminal UI for browsing and running programs from a local Assembly64 collection
- **Load Mode** - Upload and run individual files (PRG, CRT, D64, SID, etc.) from local paths or remote URLs
- **FTP Mode** - Transfer files to the C64 Ultimate's filesystem via FTP
- **Poke Mode** - Modify C64 memory addresses (useful for cheats and memory tricks)
- **Server Mode** - Host a lightweight protocol server for the C64 client application, with an optional Spiffy-compatible HTTP API so the Ultimate firmware's stock Assembly64 browser can use this server as its upstream
- **Debug Mode** - Remote screen peek, key injection, and machine control for debugging the native C64 client

### a64browser (C64 native application)

A native C64 program that runs directly on the C64 Ultimate. It connects to the `c64uploader` server over the network, enabling you to browse and launch programs from your Assembly64 collection entirely from the C64 itself—no PC interaction needed once the server is running.




## c64uploader Usage

The `c64uploader` tool runs on your PC and uses a subcommand-based interface: `c64uploader <command> [options] [arguments]`.

### TUI Mode

Browse and upload files from local Assembly64 collection to C64 Ultimate using an interactive terminal UI:

```bash
./c64uploader tui [options]
```

**Options:**
- `-host <ip>` - C64 Ultimate hostname or IP address (default: `c64u`)
- `-db <path>` - Path to SQLite database file (default: `c64uploader.db` in assembly64 directory)
- `-assembly64 <path>` - Path to Assembly64 collection (default: `~/Downloads/assembly64`)
- `-v` - Enable verbose debug logging

**Controls:**
- **↑/↓** - Navigate up/down
- **Tab** - Cycle through categories
- **Return** - Load and run selected entry
- **/** - Open advanced search
- **Esc** - Clear search or quit
- **Q** - Quit
- **Ctrl+L** - Reset search and filters

**Advanced Search (press `/`):**

Press `/` to open an advanced search form with filters for:
- Category (All/Games/Demos/Music)
- Title and Group (partial text match)
- Language (german, french, english...)
- Region (PAL, NTSC)
- Engine (seuck, gkgm, bdck...)
- File type (d64, prg, crt, sid...)
- Trainer count (min/max)
- Top 200 games only
- 4K competition entries only
- Has documentation
- Has fastloader
- Cracked/original filter

### Load Mode

Upload and run a specific file (PRG, CRT, D64, SID, etc.) from a local path or remote URL:

```bash
./c64uploader load [options] <filename|url>
```

**Options:**
- `-host <ip>` - C64 Ultimate hostname or IP address (default: `c64u`)
- `-v` - Enable verbose debug logging

**Examples:**
```bash
# Load from local file
./c64uploader load -host 192.168.2.100 ~/games/space_invaders.prg

# Load from remote URL
./c64uploader load https://example.com/foo.d64
```

### FTP Mode

Upload a file to C64 Ultimate via FTP from a local path or remote URL:

```bash
./c64uploader ftp [options] <filename|url> <destination>
```

**Options:**
- `-host <ip>` - C64 Ultimate hostname or IP address (default: `c64u`)
- `-v` - Enable verbose debug logging

**Examples:**
```bash
# Upload from local file
./c64uploader ftp -host 192.168.2.100 ~/games/space_invaders.prg /Temp/space_invaders.prg

# Upload from remote URL
./c64uploader ftp https://example.com/foo.prg /Temp
```

This is also how Ultimate firmware/flash-patch files (`.ue2`) get onto the device.
The native C64 client handles this for you: `.ue2` files placed in the server's
My Files directory (configured by `-myfiles`, defaulting to `<assembly64>/Myfiles`
— e.g. `<assembly64>/Myfiles/Firmware/`) appear under **My Files** in the browser,
and selecting one transfers it to `/Temp` via FTP **without launching** (there is
no firmware-flash REST endpoint — apply the patch from the Ultimate's on-device
menu afterward). See [c64client/README.md](c64client/README.md#running-programs).

### Poke Mode

Issue POKE commands to modify C64 memory (e.g., change border color, enable cheats):

```bash
./c64uploader poke [options] <address>,<value>
```

* `address` - Memory address to poke (0-65535 or 0x0000-0xFFFF or $0000-$FFFF)
* `value` - Value to write (0-255 or 0x00-0xFF or $00-$FF)

**Options:**
- `-host <ip>` - C64 Ultimate hostname or IP address (default: `c64u`)
- `-v` - Enable verbose debug logging

**Examples:**
```bash
./c64uploader poke 53280,0      # Decimal address and value (black border)
./c64uploader poke 0xD020,1     # Hex address with 0x prefix (white border)
./c64uploader poke $D020,2      # Hex address with $ prefix (red border)
./c64uploader poke D020,5       # Hex address without prefix (green border)
```

### Server Mode

Start the C64 protocol server for the C64 client:

```bash
./c64uploader server [options]
```

**Options:**
- `-host <ip>` - Fallback C64 Ultimate hostname or IP address (default: `c64u`). For cart connections on `-port`, the server auto-detects the target Ultimate from the cart's TCP peer IP (UCI runs on the C64 but the Ultimate firmware owns the network stack, so the inbound connection's source IP *is* the Ultimate's IP). That makes multiple Ultimates on the LAN work out of the box — each cart's RUN/RUNFILE/UPLOADFILE/DOWNLOAD routes back to its own Ultimate. `-host` is only consulted when there's no peer to infer from (the Spiffy HTTP listener and the standalone `tui`, `load`, `ftp`, `poke`, `debug` subcommands).
- `-db <path>` - Path to SQLite database file (default: `c64uploader.db` in assembly64 directory)
- `-assembly64 <path>` - Path to Assembly64 collection (default: `~/Downloads/assembly64`)
- `-myfiles <path>` - Path to the My Files directory exposed to the C64 client's **My Files** browser (default: `<assembly64>/Myfiles`). `~/` is expanded. Subdirectories are walked when the user navigates into them — e.g. drop `.ue2` firmware patches in `<assembly64>/Myfiles/Firmware/` to see them there.
- `-port <port>` - C64 protocol server port (default: `6465`)
- `-spiffy-http-port <port>` - Spiffy-compatible HTTP API port (default: `0` = disabled). When set, a second listener on this port serves the same `/leet/search/` REST API the Ultimate firmware's built-in Assembly64 browser expects, backed by our SQLite index. Lets the stock firmware browser use this server as its upstream — no custom C64 cart needed. See `uploader/SPIFFY_HTTP_API.md` for the wire format.
- `-spiffy-client-id <value>` - Optional `Client-Id` header value the Spiffy listener requires (default: empty = unauthenticated).
- `-reindex-interval <duration>` - Poll the Assembly64 directory this often and hot-swap a freshly rebuilt database when files are added or changed (default: `0` = disabled, e.g. `1h`). NFS-friendly: it polls directory mtimes rather than relying on inotify (which doesn't see changes made on an NFS server or by other clients). On a detected change it rebuilds the index in the background and swaps it in atomically — no restart, no downtime. Detects new release folders and new files inside existing folders; in-place edits of an existing file's contents are not detected. The server also rebuilds the index **once at startup** when this flag is set, so changes made while the server was down are picked up without needing a separate `sqlitegen` run; pick an interval based on how often new files land while the server is running.
- `-config <path>` - Load defaults from a flat `key = value` config file (see below). Any CLI flag passed alongside `-config` overrides the file's value for that key.
- `-v` - Enable verbose debug logging

**Config file (`-config`):**

When invocation lines get long (systemd units, NAS deployments), put the steady values in a file:

```ini
# /etc/c64uploader.conf
host = c64u.alanara.fi
assembly64 = /data/Retro/Assembly64
myfiles = /data/Retro/Assembly64/Myfiles
spiffy-http-port = 8000
reindex-interval = 1h
# v = true
```

- One `key = value` per line. `#` starts a comment to end-of-line. Blank lines OK. Whitespace around `=` ignored.
- Keys match the flag names exactly (`spiffy-http-port`, not `spiffy_http_port`). Boolean flags accept `true` / `false` / `1` / `0`.
- Precedence: defaults → config file → CLI flags. Anything you pass on the command line overrides the file.
- Unknown keys, a missing file, or a malformed line all fail loudly with file:line context so typos in production configs don't get silently ignored.

**Example:**
```bash
./c64uploader server -host 192.168.2.100 -assembly64 ~/assembly64 -port 6465 -spiffy-http-port 8000

# Auto-pick up files added to an NFS-mounted collection. The server rebuilds the
# index once at startup, then polls hourly for additions made while it's running.
./c64uploader server -assembly64 /nas/Retro/Assembly64 -reindex-interval 1h

# Same thing but with the settings parked in a config file
./c64uploader server -config /etc/c64uploader.conf
```

The C64 protocol is a simple line-based protocol optimized for low-bandwidth C64 communication.
See `uploader/C64PROTOCOL.md` for protocol details, or `uploader/SPIFFY_HTTP_API.md` for the Spiffy-compatible HTTP API.

#### Using this server with the Ultimate firmware's stock Assembly64 browser

When `-spiffy-http-port` is set, the server speaks the same `/leet/search/` REST API the Ultimate firmware's built-in Assembly64 search browser expects. Point the firmware at this server instead of `assembly64.com` and the stock browser becomes a fully working client — search, drill-down, mount/run, all backed by your local SQLite index. **No custom C64 cart required for this path.**

To configure the Ultimate, edit its **Assembly Search Servers** JSON config to point at your machine:

```json
{
  "host": "192.168.2.66",
  "port": 8000,
  "url-search":   "/leet/search/aql/0/100?query=",
  "url-patterns": "/leet/search/aql/presets",
  "url-entries":  "/leet/search/entries",
  "url-download": "/leet/search/bin"
}
```

Then start the server with the matching port:

```bash
./c64uploader server -assembly64 ~/assembly64 -spiffy-http-port 8000
```

The same daemon also runs our native cart's TCP line server on `-port` (default `6465`), so both clients can talk to the same SQLite index simultaneously.

#### Mobile web browser UI

When `-spiffy-http-port` is set, the same listener also serves a mobile-friendly single-page web UI at `/`. Point a phone's browser at `http://<host>:<port>/` and you get the same hierarchy the cart shows — categories → sources → Browse A-Z / Top 200 → entry lists → tap to run. PRG / CRT / D64 / SID launch through the C64 Ultimate's REST API exactly like the cart's run path.

| Categories | Sources | Source menu | Letter grid | Entry list |
|:---:|:---:|:---:|:---:|:---:|
| <img src="screenshots/web-01-categories.png" alt="Categories" width="180"> | <img src="screenshots/web-02-sources.png" alt="Sources" width="180"> | <img src="screenshots/web-03-source-menu.png" alt="Source menu" width="180"> | <img src="screenshots/web-04-letter-grid.png" alt="A-Z letter grid" width="180"> | <img src="screenshots/web-05-entry-list.png" alt="Entry list with release/trainer badges" width="180"> |

```
http://192.168.2.66:8000/    # phone-friendly browser UI
http://192.168.2.66:8000/leet/search/aql/presets  # Spiffy API for stock firmware
http://192.168.2.66:8000/api/menu?path=Games      # JSON API for the web UI
```

The web UI's JSON endpoints (`/api/menu`, `/api/list`, `/api/search`, `/api/info/<id>`, `POST /api/run/<id>`) reuse the cart server's existing handlers, so the three clients (cart, stock firmware, phone) all stay in lockstep on hierarchy/search/run semantics.

### Database Generator

Generate SQLite database from your Assembly64 collection for fast loading and full-text search:

```bash
./c64uploader sqlitegen [options]
```

**Options:**
- `-assembly64 <path>` - Path to Assembly64 data directory (required)

**Example:**
```bash
./c64uploader sqlitegen -assembly64 ~/assembly64
```

This creates `c64uploader.db` in the assembly64 directory containing:
- All entries (Games, Demos, Music, Intros, Graphics, Discmags)
- Full-text search index (FTS5)
- Pre-computed menu navigation hierarchy
- Grouped entries by normalized title

### Debug Mode

Remotely inspect and drive the native C64 client running on a real C64 Ultimate. Useful when a menu navigation issue is hard to reproduce by hand — the tool reads the C64's text screen back as ASCII and injects keys through a small debug byte baked into `a64browser`. No extra setup on the Ultimate is needed beyond its standard HTTP API.

```bash
./c64uploader debug <subcommand> [options]
```

**Subcommands:**
- `screen` - Read the current 40x25 text screen and render it as ASCII
- `press <key>[,<key>...]` - Inject one or more keys (serialized, one at a time)
- `hold <direction>` - Simulate a held physical key (`down`, `up`, `left`, `right`) to exercise auto-repeat
- `release` - Stop a simulated hold
- `scroll-rate <direction> <sec>` - Hold the key for N seconds and report observed rows/second
- `peek <hexaddr> <len>` - Hex+ASCII dump of N bytes starting at address
- `peekstr <hexaddr> [maxlen]` - Read up to maxlen bytes and print as a NUL-terminated C string
- `reset` - Soft reset the C64
- `reboot` - Reboot the Ultimate firmware
- `menu` - Press the Ultimate menu button
- `info` - Probe device reachability

**Options** (all subcommands):
- `-host <ip>` - C64 Ultimate hostname or IP address (default: `c64u`)
- `-v` - Enable verbose debug logging
- `-delay <duration>` - Delay between keys in `press` sequences (default: 80ms)

**Key tokens for `press`:** `up`, `down`, `next`, `prev`, `info`, `enter`, `back`, `right`, `tab`, `space`, `q`, `c`, `slash`. Single characters (A-Z, 0-9, etc.) are passed through verbatim — lowercase for nav handlers (`i`, `n`, `p`, `r`, `l`) and uppercase for search-mode typing.

**Examples:**
```bash
# Peek the current screen
./c64uploader debug screen -host c64u

# Navigate: down, down, enter (e.g. to walk into a category)
./c64uploader debug press -host c64u down,down,enter

# Measure auto-repeat scroll speed over a 2-second hold of cursor-down
./c64uploader debug scroll-rate -host c64u down 2

# Dump menu_path in the running a64browser (address from build/a64browser.lbl)
./c64uploader debug peekstr -host c64u 6100
```

**Metadata extracted:**
- Title, group, and release name
- Crack information (trainers, flags like docs/fastload/highscore)
- Language, region, and game engine
- Top 200 ranking
- 4K competition status
- Year, party, competition, and placement
- CSDB rating

See `docs/sqlite-database-schema.md` for the complete database schema specification.

## a64browser (C64 Client)

The `a64browser` is a native C64 application located in the `c64client/` directory. It runs directly on your C64 Ultimate and connects to the `c64uploader` server over the network, allowing you to browse and launch programs from your Assembly64 collection.

**Prerequisites:** Network and FTP File Service must be enabled on your C64 Ultimate.


### Running a64browser on C64

1. Build the client, **or use the prebuilt binaries** in [c64client/dist/](c64client/dist/) (`a64browser.prg` and `a64browser.crt`). Building requires oscar64 and is described in [c64client/README.md](c64client/README.md):
   ```bash
   cd c64client && make prg crt
   # produces build/a64browser.prg and build/a64browser.crt (EasyFlash)
   ```

2. Start the protocol server on your PC:
   ```bash
   ./c64uploader server -host <ultimate-ip> -assembly64 <path>
   ```

3. Launch the browser on your C64 Ultimate. Three ways:
   - **PRG + load**: `./c64uploader load -host <ultimate-ip> c64client/dist/a64browser.prg` — uploads and runs in one shot. Recommended.
   - **Cart (EasyFlash)**: `./c64uploader load -host <ultimate-ip> c64client/dist/a64browser.crt` — same flow but the Ultimate runs it as a REU-aware EasyFlash cartridge. Useful for fast cold-boot and the option to flash a real EasyFlash so the C64 powers up directly into the browser. Requires the firmware setting `Modem Settings → ACIA (6551) Mapping = Off` (the default `DE00/NMI` SwiftLink intercepts the cart's bank-control writes). F7 cleanly drops back to BASIC.
   - **FTP + manual launch**: copy the .prg to the Ultimate's filesystem and load it from BASIC.

4. On startup:
   - The browser checks the Ultimate, captures the firmware ID and IP, then loads `/flash/config/a64browser.cfg`.
   - If the saved `AUTOSTART` flag is on, it connects silently and lands on the root menu. Otherwise (or on connect failure) it shows the **config screen** with the error in the status bar.
   - **F1** opens the config screen at any time. **F7** exits — pops the Ultimate menu on the .prg target, drops to BASIC on the EasyFlash cart.

### Config Screen

The first screen on a fresh install (and the screen you reach with F1):

```
ASSEMBLY64 (LOCAL)

ULTIMATE-II DOS V1.2
IP: 192.168.2.64

> SERVER:    192.168.2.66
  PORT:      6465
  AUTOSTART: YES
  .CONNECT.
  .SAVE.
```

- **W/S** or cursor up/down — move between fields
- **Return** on `SERVER` / `PORT` — edit the value (Return to commit)
- **Space** on `AUTOSTART` — toggle YES / NO
- **Return** on `.CONNECT.` — connect with current values (does not save)
- **Return** on `.SAVE.` — write `/flash/config/a64browser.cfg` and connect
- **F7** — exit (back to Ultimate menu)

The settings file is a plain three-line text file: `host\nport\nautostart\n` where `autostart` is `0` or `1`.

### Controls

**Category list (and letter grid):**
- **W/S** or cursor up/down - Navigate rows (step by one in lists, step by a grid row in the A-Z grid)
- **Return** or right arrow - Enter category
- **/** - Search mode (from root)
- **F1** - Config screen
- **F7** - Exit (PRG: pop Ultimate menu; EasyFlash: drop to BASIC)

When a category menu includes `BROWSE A-Z`, selecting it opens a **27-cell letter grid** (A..Z plus `#` for titles that don't start with a letter). The grid has its own navigation:
- **W/S** or cursor up/down - Move between grid rows (9 cells per row)
- **Cursor left/right** or **A/D** - Move left/right within a row
- **Return** - Enter the selected letter's entry list
- **Cursor left on column 0** (A, J, or S) - "Wall bump" back to the source menu
- **DEL** - Back to the source menu

**Entry list:**
- **W/S** or cursor keys - Navigate up/down (auto-advances pages at top/bottom of visible list)
- **Return** - Run selected entry (or open Releases if the entry has multiple versions)
- **I** - View entry info (name, group, year, type, trainers)
- **N/P** - Next/Previous page
- **right arrow** - Open Releases for entries with multiple versions
- **DEL** or left arrow - Back to parent menu

**Search mode (`/` from root):**
- Type to build a query (minimum 3 characters)
- **Tab** - Cycle category filter (All/Games/Demos/Music)
- **Return** or cursor down - Run the search and switch focus to the result list
- **Cursor up** at the top of the results (or **DEL**) - Return to the input box, query preserved
- **I** - View entry info from the result list
- **DEL** in the input box - Delete a character, or exit to categories when empty

**Config screen (F1 from anywhere):**
- **W/S** - Navigate fields
- **Return** - Edit field, run action (`.CONNECT.` / `.SAVE.`)
- **Space** - Toggle `AUTOSTART`
- **DEL** - Delete character (when editing) or back to previous page
- Numbers and `.` - Enter IP address / port

## Credits

Joint project — two repos kept in sync, contributions flow both ways.

- **[@tsaarni](https://github.com/tsaarni)** ([tsaarni/c64uploader](https://github.com/tsaarni/c64uploader)) — original author and primary developer of the `c64uploader` Go server
- **[@jusii](https://github.com/jusii)** ([jusii/c64uploader](https://github.com/jusii/c64uploader)) — `a64browser` native C64 cartridge / EasyFlash client (Oscar64)

## License

This repository is dual-licensed along directory boundaries:

- **[`uploader/`](uploader/)** (the Go server) and the rest of the top-level project — **Apache License 2.0**, see [LICENSE](LICENSE).
- **[`c64client/`](c64client/)** (the `a64browser` native C64 client) — **GNU General Public License v3 or later**, see [c64client/LICENSE](c64client/LICENSE). This subdirectory is GPL because [c64client/src/ultimate.c](c64client/src/ultimate.c) and [c64client/src/ultimate.h](c64client/src/ultimate.h) are ported from [xlar54/ultimateii-dos-lib](https://github.com/xlar54/ultimateii-dos-lib) (GPL v3), and the linked binary inherits that license.

The two components are independent processes that talk over a network socket — the GPL on the C64 side does not extend to the Apache-licensed Go server.
