# Assembly64 Browser - C64 Client

Native C64 client for browsing the Assembly64 database via Ultimate II+ network interface.

> **License: GPL v3.** This subdirectory is licensed under the GNU General Public License v3 (see [LICENSE](LICENSE)) because [src/ultimate.c](src/ultimate.c) and [src/ultimate.h](src/ultimate.h) are ported from [xlar54/ultimateii-dos-lib](https://github.com/xlar54/ultimateii-dos-lib) (GPL v3). The rest of the c64uploader project (the Go server under [`uploader/`](../uploader/)) remains under Apache 2.0 — see the [top-level README](../README.md#license) for details.

## Requirements

- **oscar64** compiler - https://github.com/drmortalwombat/oscar64
- **Ultimate II+** or **Ultimate 64** with network enabled
- Optionally: VICE emulator for testing

## Building

```bash
# Build PRG file
make prg

# Build EasyFlash cartridge (REU-aware subtype 1) — the cart we ship
make crt

# Build D64 disk image (requires VICE c1541)
make d64
```

`make crt` adds `-tf=crt -csub=1` (EasyFlash, REU-aware subtype 1). The vanilla 16 KB CRT16 slot can't hold the binary anymore, so the EasyFlash format is the only cart target. `-dNOFLOAT` is on by default — we never format floats, and dropping the oscar64 float-printf helpers saves ~1.6 KB. Both `.prg` and `.crt` are supported deployments; the cart requires the firmware setting `Modem Settings → ACIA (6551) Mapping = Off`. See [docs/architecture-c64client.md](../docs/architecture-c64client.md#easyflash--tfcrt-build-target) for the full investigation.

Prebuilt binaries are committed under [dist/](dist/) so users can deploy without installing oscar64.

### Notes on the cart

- The cart contains exactly the same browser as the .prg. There is no
  feature gap in the source.
- The cart bypasses the C64 KERNAL boot path; the program therefore
  initializes the VIC chip (display on, text mode, default screen
  pointers) and runs `init_state()` to set the few non-zero defaults
  at runtime. The EasyFlash boot stub LZ-decompresses the data section
  into RAM, so static variables with explicit initializers work fine.
  See [c64client/src/main.c](src/main.c) for the pattern.
- The EasyFlash format is what makes the cart format viable at all:
  cart bank 0 holds an LZ-compressed snapshot that decompresses into
  ~30 KB of low RAM, well past the 16 KB the binary now needs.

## Running

### In VICE emulator

```bash
make run                # launches x64sc with the .prg
```

### On real hardware

```bash
# Upload the .prg to the Ultimate's filesystem and load it from BASIC
U2P_HOST=192.168.1.x make deploy

# Run the .prg directly via the Ultimate REST API (one-shot)
U2P_HOST=192.168.1.x make runprg

# Run the EasyFlash .crt directly via the Ultimate REST API
U2P_HOST=192.168.1.x make runcrt

# Or use the prebuilt binaries with the c64uploader CLI:
./c64uploader load -host <ultimate-ip> dist/a64browser.prg
./c64uploader load -host <ultimate-ip> dist/a64browser.crt
```

The .crt is what you want if you ever flash a real EasyFlash / KFF /
similar — the C64 boots straight into the browser on power-on with no
PC involvement.

## Project Structure

```
c64client/
├── src/
│   ├── main.c        - Main client application
│   ├── ultimate.h    - Ultimate II+ library header
│   └── ultimate.c    - Ultimate II+ library (ported from cc65)
├── build/            - Build output (gitignored)
├── dist/             - Prebuilt artifacts (a64browser.prg, a64browser.crt)
├── Makefile
└── README.md
```

## Ultimate II+ Library

The `ultimate.h/c` library provides access to Ultimate II+ Command Interface:

### Network functions
- `uci_tcp_connect(host, port)` - Connect to TCP server
- `uci_socket_read(socket, length)` - Read from socket
- `uci_socket_write(socket, data)` - Write to socket
- `uci_socket_close(socket)` - Close connection

### Convenience functions
- `uci_tcp_nextchar(socket)` - Read single character
- `uci_tcp_nextline(socket, buffer)` - Read line

### DOS functions
- `uci_identify()` - Check Ultimate presence
- `uci_getipaddress()` - Get IP address
- `uci_change_dir(path)` - Change directory
- `uci_open_file(attrib, name)` - Open file
- etc.

## Usage Guide

### Startup and Connect

On launch the browser silently probes the Ultimate (firmware ID + own IP), loads `/flash/config/a64browser.cfg`, and:

- if `AUTOSTART=YES` and the saved server is reachable → connects and lands on the root category list
- otherwise → shows the **config screen** with diagnostics at the top and editable rows for `SERVER`, `PORT`, `AUTOSTART`, plus `.CONNECT.` and `.SAVE.` actions. Any connection error is printed in the status bar so you know what to fix.

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

| Key | Action |
|-----|--------|
| **W / cursor up**, **S / cursor down** | Move between fields |
| **Return** on `SERVER` / `PORT` | Edit value (Return again commits, DEL erases a character) |
| **Space** on `AUTOSTART` | Toggle YES / NO |
| **Return** on `.CONNECT.` | Connect with current in-memory values (does not write to disk) |
| **Return** on `.SAVE.` | Write the config file and connect |
| **F1** | Open config from any other page |
| **F7** | Exit — pops the Ultimate menu on the .prg target, drops to BASIC on the EasyFlash cart |

The config file is plain text:
```
192.168.2.66
6465
1
```
(host, port, autostart 0/1). It lives at `/flash/config/a64browser.cfg`, which matches the prkl/Spiffy convention; the directory is auto-created on first save.

### Category Tree

Once connected, you land on the root category list (`ASSEMBLY64 - CATEGORIES`). Navigation in menus:

| Key | Action |
|-----|--------|
| **W / cursor up** | Move cursor up |
| **S / cursor down** | Move cursor down |
| **Return / cursor right** | Enter category or folder |
| **DEL / cursor left** | Back to parent menu |
| **/** | Open search (from root) |
| **F1** | Open config screen |
| **F7** | Exit — pops the Ultimate menu on .prg, drops to BASIC on EasyFlash |

The tree is: **Category → Source → [Browse A-Z, Top 200, Top 500, ...]**, e.g. `Games → CSDB → Browse A-Z`.

### A-Z Letter Grid

Selecting **Browse A-Z** opens a 27-cell grid (A..Z plus `#` for non-letter titles):

```
> A   B   C   D   E   F   G   H   I
  J   K   L   M   N   O   P   Q   R
  S   T   U   V   W   X   Y   Z   #
```

| Key | Action |
|-----|--------|
| **W / cursor up** | Move up one grid row (-9 cells) |
| **S / cursor down** | Move down one grid row (+9 cells) |
| **cursor right / D** | Move right within the row |
| **cursor left / A** | Move left within the row; on column 0 (A, J, S) wall-bumps back to the source menu |
| **Return** | Enter the selected letter's entry list |
| **DEL** | Back to source menu |

### Browsing Entry Lists

When viewing a list of entries (from a letter, a Top 200, a search result, etc.):

| Key | Action |
|-----|--------|
| **W/S or cursor keys** | Move cursor; auto-pages at top/bottom |
| **N / P** | Next / previous page (20 per page) |
| **Return** | Run entry (or open Releases if multiple versions exist) |
| **cursor right** | Open Releases for a grouped entry |
| **I** | Show info (title, group, year, type, top200, rating) |
| **DEL / cursor left** | Back to parent menu |

Entries display additional indicators:

| Indicator | Meaning |
|-----------|---------|
| **>** (green) | Multiple releases — press cursor-right to view them |
| **+N** (green) | Trainer count |
| **&D** | Documentation available |

### Search

Press **`/`** from the root menu to open search. The cursor starts in the input box; type to build a query (minimum 3 characters), Tab cycles the category filter (All / Games / Demos / Music). **Return** or cursor-down switches to the result list; cursor-up at the top of the results (or **DEL**) returns to the input box. Two-mode design keeps held letters from corrupting the query during result navigation.

### Grouped Entry Indicators

In category browsing and advanced search results, entries display additional indicators:

| Indicator | Meaning |
|-----------|---------|
| **+N** (green) | Trainer count (e.g., +3 means 3 trainers available) |
| **>** (green arrow) | Multiple releases exist - press **>** key to see all releases |

When viewing a grouped entry:
- Press **>** to expand and see all releases of that title
- Each release shows the cracker group and trainer count

### Releases View

When expanding a grouped entry (pressing **>**), you see all releases:
- Shows cracker/release group name
- Displays trainer count (+N) if available
- Press **Return** to run a specific release
- Press **Del** to go back to the previous list

### Info Screen

Press **I** on any entry to see detailed information:
- Title, Group, Year
- File type and size
- Category
- Top 200 rank (if applicable)
- Trainer information (for games)

Press any key to return to the previous screen.

### Running Programs

When you press Return on an entry, the server uploads the file to your Ultimate via its REST API and dispatches by extension:

| Type | Action |
|------|--------|
| **PRG** | Loaded and run directly (`/v1/runners:run_prg`) |
| **CRT** | Cartridge mounted (`/v1/runners:run_crt`) |
| **D64/G64/D71/D81/G71** | Disk mounted, first program auto-loaded (`/v1/runners:mount_d64`) |
| **SID** | Played via the Ultimate's sandboxed SID player (`/v1/runners:sidplay`) — tune only, no demo graphics |

Files you drop into the server's `myfiles/` directory show up under **My Files** in the
category tree and run the same way. One exception: **`.ue2` firmware/flash-patch files**
(e.g. `Prkl_FlashPatch_1.1.0s2p7.ue2`) are *upload-only*. Selecting one transfers it to the
Ultimate's `/Temp` directory via FTP (status shows `uploading...` → `OK Uploaded /Temp/...`)
and does **not** launch anything — there is no firmware-flash REST endpoint, so you apply the
patch yourself from the Ultimate's on-device menu afterward.

### Tips

- The client remembers your position when paging through results
- Search is case-insensitive
- In advanced search, use `top200=1` to show only Top 200 entries
- Trainer indicators help you find versions with cheats/trainers

## Server Protocol

See [`../uploader/C64PROTOCOL.md`](../uploader/C64PROTOCOL.md) for details.

## Configuration

Compile-time defaults in `main.c`:
```c
#define DEFAULT_SERVER_HOST "192.168.2.66"
#define DEFAULT_SERVER_PORT 6465
#define SETTINGS_FILE       "/flash/config/a64browser.cfg"
```

Users change settings at runtime via the config screen (F1) — server host, port, and autostart are persisted to `/flash/config/a64browser.cfg` and reloaded on next launch. Only the compile-time defaults need recompilation.

## Debug Hooks

The client reserves two KERNAL scratch bytes for remote driving via the Ultimate's HTTP API (see `c64uploader debug` in the top-level README):

| Address | Name | Purpose |
|---------|------|---------|
| `$02A7` | `DEBUG_KEY_INJECT` | Single-shot key press consumed by `get_key()` / `wait_key()` |
| `$02A8` | `DEBUG_HOLD_SCAN` | Simulated matrix-level held key (bypasses `keyb_poll()`) |

Both bytes are zero in normal operation. They let a PC tool peek the text screen, inject keys, and measure auto-repeat timing without physical access to the keyboard.

## Credits

- Ultimate II+ library ported from [ultimateii-dos-lib](https://github.com/xlar54/ultimateii-dos-lib) by Scott Hutter, Francesco Sblendorio
- Oscar64 compiler by DrMortalWombat
