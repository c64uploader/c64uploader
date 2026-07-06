# Architecture: C64 Native Client (c64client)

> Native Commodore 64 application for browsing Assembly64 content via Ultimate II+ network interface.

## Executive Summary

The c64client is a native C64 application compiled with Oscar64 that provides a full-featured browser interface running directly on Commodore 64 hardware. It communicates with the Go server over TCP via the Ultimate II+ Command Interface (UCI).

## Technology Stack

| Category | Technology | Purpose |
|----------|------------|---------|
| Language | C (Oscar64) | C64-optimized C compiler |
| Target | 6502 CPU | Commodore 64 processor |
| Hardware | Ultimate II+ | Network-enabled cartridge |
| Network | TCP/IP | Via UCI API |
| Build | Make | Multi-target build system |
| Testing | VICE (x64sc) | C64 emulator |

## Architecture Pattern

**Single-file Embedded Application with Hardware Abstraction**

```
┌─────────────────────────────────────────────────────┐
│                   main.c (~2330 LOC)                │
│  ┌─────────────────────────────────────────────────┐│
│  │  Application Layer                              ││
│  │  - Menu system, navigation, search              ││
│  │  - VIC-II screen handling                       ││
│  │  - User input processing                        ││
│  └─────────────────────────────────────────────────┘│
│  ┌─────────────────────────────────────────────────┐│
│  │  Protocol Layer                                 ││
│  │  - TCP line buffering                           ││
│  │  - Command/response parsing                     ││
│  │  - Field extraction                             ││
│  └─────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────────┐
│           ultimate.c/h (UCI Library)               │
│  - TCP socket management                           │
│  - DOS operations                                  │
│  - Hardware identification                         │
└─────────────────────────────────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────────┐
│        Ultimate II+ Command Interface (UCI)        │
│        Memory-mapped registers at $DF1C-$DF1F      │
└─────────────────────────────────────────────────────┘
```

## Source Structure

```
c64client/
├── src/
│   ├── main.c        # Main application (~2330 lines)
│   ├── ultimate.c    # UCI library implementation
│   └── ultimate.h    # UCI library header
├── build/            # Output directory
└── Makefile          # Build system
```

## Memory Constraints

**Critical Constants (main.c):**
```c
#define MAX_ITEMS 32         // Items per menu buffer (must fit the 27-cell A-Z letter grid)
#define LINE_BUFFER_SIZE 128 // TCP line buffer
```

**Per-item Memory** (per `MAX_ITEMS`=32):
- `item_names[32][32]` = 1024 bytes
- `menu_paths[32][48]` = 1536 bytes
- `item_cats[32][8]`  = 256 bytes
- Total per-screen: ~3KB

**Total usable RAM:** ~38KB (C64 has 64KB, minus OS, screen, etc.)

Lists themselves are still paginated at 20 entries per server page; MAX_ITEMS=32 exists so the A-Z letter grid (27 cells) fits in the same buffers.

## Key Components

### Main Application (main.c)

**Page State (current_page):**
```c
#define PAGE_CATS        0   // Category / source / letter-grid menu
#define PAGE_LIST        1   // Entry list (LISTPATH result)
#define PAGE_SEARCH      2   // Two-mode typed search (input box / result list)
#define PAGE_SETTINGS    3   // Config screen — diagnostics + editable fields
#define PAGE_INFO        6   // Entry info screen
#define PAGE_RELEASES    7   // Releases of a title
```

The former PAGE_ADV_SEARCH / PAGE_ADV_RESULTS pages were removed; the simple typed search at PAGE_SEARCH covers the same need against the server's `SEARCH` command. The previous splash screen was also removed — its job (Ultimate detection + IP capture + connect) is now done silently before any UI is drawn, with PAGE_SETTINGS doubling as the first-run / failed-autostart screen.

**Back-navigation via path trimming:**

There is no navigation stack. The current menu path (`menu_path`) IS the history — going back trims the last `/`-separated component and reloads that parent menu. This removes a whole class of "stack-and-path-out-of-sync" bugs and keeps per-frame RAM use tiny.

```c
static char menu_path[64];        // e.g. "Games/CSDB/A-Z"
static char current_category[48]; // list path, e.g. "Games/CSDB/A-Z/A"

static void trim_path(char *path);  // "Games/CSDB" -> "Games", "Games" -> ""

void go_back(void);  // reloads menu_path (or trims first if we're on a menu)
```

**Screen Areas:**
- Title bar (row 0)
- Content area (rows 1-22)
- Status bar (row 24)

### UCI Library (ultimate.c/h)

**Network Functions:**
```c
unsigned char uci_tcp_connect(const char *host, unsigned short port);
unsigned char uci_socket_read(unsigned char socket, unsigned short length);
void uci_socket_write(unsigned char socket, const char *data);
void uci_socket_close(unsigned char socket);
```

**Convenience Functions:**
```c
char uci_tcp_nextchar(unsigned char socket);
int uci_tcp_nextline(unsigned char socket, char *buffer);
```

**DOS Functions:**
```c
unsigned char uci_identify(void);
char* uci_getipaddress(void);
void uci_change_dir(const char *path);
unsigned char uci_open_file(unsigned char attrib, const char *name);
```

### Screen Handling

**VIC-II Direct Access:**
```c
#define SCREEN_RAM  ((unsigned char*)0x0400)
#define COLOR_RAM   ((unsigned char*)0xD800)

void print_at(int x, int y, const char *text);
void clear_line(int y);
void set_color(int x, int y, unsigned char color);
```

**Custom Character Display:**
- Reverse video for selection
- Color coding: Green for trainers, White for normal
- Special characters for indicators (>, +)

### Protocol Communication

**Request Format:**
```c
void send_command(const char *cmd) {
    uci_socket_write(socket, cmd);
    uci_socket_write(socket, "\n");
}
```

**Response Parsing:**
```c
// Read header: "OK <count>" or "ERR <message>"
uci_tcp_nextline(socket, buffer);

// Parse fields: "type|field1|field2|..."
while (uci_tcp_nextline(socket, buffer) > 0) {
    if (buffer[0] == '.') break;  // End marker
    parse_fields(buffer);
}
```

## User Interface

### Main Menu

There is no numbered "main menu" — the root page is the live category list loaded from the server's `MENU` command (`Games`, `Demos`, `Music`, `Intros`, `Graphics`, `Discmags`, `My Files`). The first screen on a fresh install is the [config screen](#configuration), reached again with F1 from anywhere.

### Navigation Keys

| Key | Action (list / menu) | Action (letter grid) |
|-----|----------------------|-----------------------|
| W / cursor up | Row up | Grid up (-9 cells) |
| S / cursor down | Row down | Grid down (+9 cells) |
| cursor right (no shift) | Enter folder / open releases | Grid right (+1) |
| cursor left (shift+right) | Back (in menus/lists) | Grid left (-1); wall-bump back at column 0 |
| A / D (grid only) | - | Grid left / right |
| Return | Select / Run / drill | Enter the selected letter's list |
| N / P | Next / previous page (list only) | - |
| I | Show info (list only) | - |
| / | Open simple typed-query search | Open simple typed-query search |
| F1 | Config screen | Config screen |
| F7 | Exit (pops Ultimate menu on .prg, drops to BASIC on EF) | Same as left column |
| DEL | Back | Back |

### Letter Grid Mode

When a menu's path ends in `/A-Z`, it renders as a 3-row × 9-column grid of letter cells instead of a vertical list. Cursor moves repaint only the outgoing and incoming cells (via `draw_letter_cell`) — no full-screen redraw. The grid is always 27 cells (A..Z plus `#`) regardless of how many letters actually have entries.

### Keyboard Auto-Repeat

Oscar64's `keyb_poll()` is edge-triggered: `keyb_key` reports a new press but then returns zero on every subsequent poll while the key is held. Auto-repeat is implemented on top of this by consulting `keyb_matrix[]` (level state) via `key_pressed(last_key_scan)` after every poll. The jiffy clock byte at `$A2` drives timing: 16 jiffies (~0.32s) initial delay, then 4 jiffies (~80ms, ~12 Hz) between repeats. Tuned to feel responsive without overshooting target rows.

### Debug Hooks

Two reserved KERNAL scratch bytes let an external uploader tool (`c64uploader debug ...`) drive the client over the C64 Ultimate HTTP API:

| Address | Name | Purpose |
|---------|------|---------|
| `$02A7` | `DEBUG_KEY_INJECT` | Non-zero value is consumed by `get_key()`/`wait_key()` as a single-shot key press, then cleared. Used for `debug press` sequences. |
| `$02A8` | `DEBUG_HOLD_SCAN` | Non-zero scancode (low 6 bits + bit 6 = shift) simulates a matrix-level held key, bypassing `keyb_poll`. Used for `debug hold` and `debug scroll-rate` to exercise auto-repeat timing deterministically. |

Both are zero in normal operation and do not interfere with physical keyboard input.

### Visual Indicators

| Indicator | Meaning |
|-----------|---------|
| `>` (white) | Cursor / selected item |
| ` >` suffix on entry name | Multiple releases available for this title |
| `+N` (green) | Trainer count |
| `&D` | Documentation available |

## Data Flow

```
┌────────────────────┐
│   User Input       │
│   (keyboard)       │
└─────────┬──────────┘
          │
          ▼
┌────────────────────┐
│   State Machine    │
│   (main.c)         │
└─────────┬──────────┘
          │
          ▼
┌────────────────────┐     TCP/6465    ┌────────────────────┐
│   Protocol Layer   │◄───────────────►│   Go Server        │
│   (command/parse)  │                 │   (uploader)       │
└─────────┬──────────┘                 └────────────────────┘
          │
          ▼
┌────────────────────┐
│   UCI Library      │
│   (ultimate.c)     │
└─────────┬──────────┘
          │
          ▼
┌────────────────────┐
│   Ultimate II+     │
│   Hardware         │
└────────────────────┘
```

## Build System

**Targets:**
```makefile
make prg      # Build .prg file (default)
make crt      # Build 16KB cartridge
make d64      # Build disk image (requires c1541)
make run      # Run in VICE emulator
make deploy   # FTP upload to Ultimate
make runprg   # Execute via REST API
make runcrt   # Execute cartridge via REST API
```

**Compiler Flags:**
- `-n` - Native code generation (faster)
- `-O2` - Optimization level 2
- `-tm=c64` - Target machine: Commodore 64
- `-dNOFLOAT` - Drop oscar64's float-printf helpers (we never format floats; saves ~1.6 KB)
- `-tf=crt -csub=1` - EasyFlash cartridge image, REU-aware subtype 1 (`make crt` — the cart we ship)

### Cart-target constraints

The cart runs from EasyFlash at `$8000-$BFFF` with the boot stub LZ-decompressing the data section into RAM at startup. Two oscar64 cart-runtime details govern how the source is written:

1. **No KERNAL data segment init.** Even with the EasyFlash boot stub handling the data copy, the C64 KERNAL is bypassed, so the program runs `init_state()` at the top of `main()` to assign the genuinely-non-zero defaults:
   - `server_host` ← `DEFAULT_SERVER_HOST` via `strcpy`
   - `server_port` ← `DEFAULT_SERVER_PORT`
   - `auto_connect` ← `false`
   - `search_in_box` ← `true`
   - `releases_return_page` ← `PAGE_LIST`
   - `last_key_scan` ← `0xFF`
2. **No KERNAL VIC init.** The KERNAL is bypassed on cart boot, so `main()` writes `$DD00`, `$D018`, `$D016`, `$D011` to put the VIC chip into a known good text-mode state (display on, screen at `$0400`, charset at `$1000`). Without this the C64 video output can stay black even though screen RAM is correct.

The .prg variant uses the regular C64 boot path where the KERNAL initializes both the data segment and the VIC; the same source code runs identically in both.

A vanilla 16 KB CRT16 cartridge isn't viable: the binary now exceeds 16 KB since the unified config screen and autostart logic landed. The shipped cartridge target is the EasyFlash variant (`make crt`); both `.prg` and `.crt` are shipped in [`c64client/dist/`](../c64client/dist/).

### EasyFlash (`-tf=crt -csub=1`) build target

`make crt` builds an EasyFlash cartridge image (`a64browser.crt`). It links, boots, and the full UCI surface works; F7 cleanly drops to BASIC. Getting there required three firmware-specific gotchas that aren't in the published EasyFlash spec or the user-facing Ultimate docs:

**1. RAM region sizing.** Out of the box `-tf=crt` fails with `error 3034: Could not place object 'X'  Size N Available 0 in section 'bss'` for every static, plus `Cannot place stack section` / `Cannot place heap section`. The errors look like a "BSS conflict" but really mean the entire `main` region is exhausted. oscar64's auto-config gives the EasyFlash format the same 16 KB region as PRG (`0x0900-0x4700`); a ~17 KB binary doesn't fit, and the linker reports it section by section.

The fix is the canonical EasyFlash layout from oscar64's `samples/memmap/easyflashshared.c`:

```c
#ifdef OSCAR_TARGET_CRT_EASYFLASH
#pragma region(main, 0x0900, 0x8000, , , {code, data, bss, heap, stack})
#endif
```

This stretches the RAM region to 30 KB (everything below the cart at $8000). The boot stub LZ-compresses this region into cart bank 0 ROM and decompresses it back into RAM at startup, so code/data/BSS/heap/stack all live in RAM after boot — same shape as PRG, just delivered through a cart. The PRG build uses oscar64's auto-configured region and ignores the pragma.

**2. CRT subtype 1 + UCI relocation.** With the pragma in place the cart links and boots — the title bar renders correctly, main() runs — but `uci_identify()` spins forever because reads from `$DF1C` return open-bus `$FF`. Two pieces in play:

- Standard EasyFlash carts (CRT subtype 0) reserve $DF00-$DFFF as 256 bytes of cart RAM. This collides with the Ultimate's default UCI mapping at $DF1C-$DF1F, and the firmware auto-disables UCI for the cart's lifetime. Cure: the EasyFlash hardware sub-type byte at CRT header offset 0x1A. Subtype 1 ("REU-aware EasyFlash") tells the firmware the ROM image will not touch $DF00-$DFFF. oscar64 supports a `-csub=1` flag that writes the subtype byte.
- With subtype 1 the Ultimate firmware does not "leave UCI alone at $DF1C" — it **relocates UCI to $DE1C-$DE1F** (I/O 1). The cart still owns all of I/O 2; UCI moves to I/O 1, where EasyFlash's $DE00/$DE02 bank-control registers don't extend up to $DE1C. The relevant flag in firmware ([GideonZ/1541ultimate commit 8e92e6d](https://github.com/GideonZ/1541ultimate/commit/8e92e6dbd5b152c80112b83a5b283e6feb984a2d)) is literally named `CART_UCI_DE1C`. This relocation isn't called out in the user-facing docs; the trail leads through the [Zak McKracken / Ultimate-64 forum64.de thread](https://www.forum64.de/index.php?thread/154897-zak-mckracken-st%C3%BCrzt-auf-dem-ultimate64-ab/=&pageNo=4).

`ultimate.h` defines the UCI register addresses conditional on `OSCAR_TARGET_CRT_EASYFLASH` so the same `ultimate.c` compiles for both the .prg case (UCI at $DF1C) and the cart case (UCI at $DE1C). The cart reaches the server, navigates menus, and exercises the full UCI surface on the test C64 Ultimate (firmware 1.1.0).

**3. F7 exit-to-BASIC.** Software-disabling the cart from inside it took two firmware-specific gotchas to get right; the published EasyFlash spec doesn't reflect what the C64 Ultimate firmware 1.1.0 actually implements:

- **ACIA modem mapping must be Off.** When the firmware's `Modem Settings → ACIA (6551) Mapping` is `DE00/NMI` (the default), the SwiftLink/ACIA emulation claims `$DE00-$DE07` and intercepts every write — including the `$DE02` bank-control writes EasyFlash needs. The cart-disable silently goes to the ACIA command register and the cart stays mapped. Set `ACIA (6551) Mapping = Off` in firmware config before running the cart, or `make runcrt` is a no-op for exit purposes.
- **The kill value is `$04`, not `$87`.** The published EF spec says writing `$87` to `$DE02` (mode + `!EXROM=1` + `!GAME=1` + LED) hides the cart. On this firmware, `$87` keeps the cart visible at `$8000-$BFFF`. The only value (out of `$00..$87` walked empirically) that hides the cart at both `$8000` and `$E000` is `$04` — bit 2 alone, every other bit zero. The screen probe routine (in commit history) reads `$8000` after each candidate and shows that `$04` is the unique winner.

With those two pieces in place, the exit sequence is just three instructions:

```
LDA #$04 / STA $DE02   ; cart goes away
JMP $FCE2              ; full KERNAL cold-reset — IOINIT, RAMTAS,
                       ; RESTOR, CINT, then JMP ($A000) → BASIC
                       ; cold-start banner + "READY." prompt
```

The full `$FCE2` path runs every initialization a hardware reset would, leaving the system indistinguishable from a fresh boot. Earlier intermediate attempts (`CINT + BASIC NEW`, `BASIC cold-start $E394`) appeared to work on the first F7 — printing "READY." — but left the system in a fragile state where a subsequent F7 keypress at the BASIC prompt could hang or corrupt the screen, because zero page, IRQ vectors, or `$0001` were not fully reinitialized. Going through `$FCE2` from a properly-disabled-cart starting state fixes that. The earlier `JMP $FCE2` attempts hung only because the previous EF disable value (`$87`) hadn't actually hidden the cart, so KERNAL was reading garbage from `$E000` on a cart still mapped to Ultimax. With `$04` the cart is genuinely off-bus before KERNAL starts.

**4. Staying inside one 16 KB bank — why not multi-bank.** The resident image (code + data) is LZ-compressed into **bank 0** at link time and decompressed into RAM at boot, so it must fit one 16 KB EasyFlash bank. As features landed the compressed image outgrew it and `make crt` started failing with `error 3034: Can not fit main region into first ROM bank` (the `.prg` build is unaffected — it has no bank limit). The obvious fix is to page self-contained subsystems (e.g. the config screen) into a second EasyFlash bank and run them from ROM, oscar64-sample style. **That does not work on the C64 Ultimate's cart emulation** — verified empirically over a long hardware debug session (2026-05-25). The cart is kept under one bank with size optimization (`-Oo -Oz`, see the Makefile) instead.

What was tried and what the hardware actually does (all probed live via the REST `readmem`/`writemem` DMA and on-cart marker bytes):

- A two-bank `.crt` builds correctly (`#pragma section(bcode1)` / `#pragma region(bank1, 0x8000, 0xc000, , 1, …)`, `__noinline` impls in `bcode1` with resident wrappers that set the `$DE00` bank register). The file's CHIP packets for bank 0 and bank 1 both carry the right content.
- The bank register **does** respond and the cart **is** loaded: with the cart mapped, the running CPU reads bank 0 `$8000`=`09 80` and bank 1 `$8000`=`a2 03 b5` (exactly `load_settings_impl`). (`run_crt` loads all banks too — an earlier "single-bank" conclusion was wrong; it came from reading bank 1 with the cart *unmapped*.)
- **The blocker:** after the oscar64 boot stub decompresses bank 0 into RAM it *disables the cart for the CPU*, so `$8000` reads RAM. Re-mapping the cart ROM for the CPU is only possible with **`$DE02 = $00`** (Ultimax) — and a direct memory test proved `$DE02=$00` **opens `$1000–$7FFF`** (write `5A` to `$2000`, read back `FF` under `$00`, `5A` again under `$04`). The 8 K/16 K control values (`$06/$07/$86/$87`) don't map the cart ROM at all on this firmware; `$04` maps neither ROM nor the UCI registers at `$DE1C` (hangs `uci_identify`).
- So there is **no `$DE02` mode that simultaneously** exposes the bank ROM at `$8000`, keeps the resident helpers in `$1000–$7FFF` readable, and keeps UCI alive — but banked code needs all three (it runs from `$8000`, calls `print_at`/`uci_*`/`sprintf` in `$1000–$7FFF`, and the browser needs UCI). The one mode that maps the bank ROM hides the very RAM the banked code calls into.

Two oscar64 footguns found along the way (worth keeping if banking is ever revisited): `<c64/easyflash.h>`'s `eflash` macro casts to a bare `EasyFlash` type that only names a type in C++ (C parse error), and a function-like macro expanding to a `$DExx` store mis-parses as a statement (`error 3006`); open-code the store instead.

**It's specifically a subtype-1 constraint, not multi-bank in general.** Multi-bank
EasyFlash works fine on the Ultimate — oscar64's own multi-bank sample
(`samples/memmap/easyflashshared.crt`, 7 banks, 14 CHIPs, same decompress-to-RAM
+ banked-ROM model as ours) is **subtype 0**, and the device ships working
multi-bank carts (Retro Replay, Final Cartridge III, …). We are forced to
**subtype 1** because that's the only mode that exposes UCI (relocated to `$DE1C`);
subtype 0 makes the firmware claim `$DF00-$DFFF` and hides UCI. Under subtype 1,
reading any bank ROM requires `$DE02=$00` (Ultimax), which opens `$1000-$7FFF`.
Because every banking mechanism needs to read a bank while resident code runs,
each hits that wall:
- runtime banked calls — the bank-map mode hides the `$1000-$7FFF` helpers the
  banked code calls;
- oscar64 **inlays** / compressed-section expand — `oscar_expand_lzo` itself lives
  in `$1000-$7FFF`, open while the bank is mapped;
- a bigger boot image — oscar64 decompresses only bank 0 into RAM, not multiple;
- disk/KERNAL overlays — the cart bypasses the KERNAL.

**If banking is ever truly needed under subtype 1**, the only viable shape is a
hand-written **low-RAM trampoline**: a copy/expand routine placed below `$1000`
(the only RAM that survives Ultimax) that pages a bank in (`$DE02=$00`), copies
the banked code into free RAM, pages back to the cart-hidden state, and runs it
from RAM where both it and the resident helpers are reachable — plus discovering
the exact cart-hidden-but-UCI-live `$DE02` value (the boot's off-state). That's a
substantial, firmware-specific asm effort and was deliberately not pursued.
`-Oo -Oz` keeps the image in one bank with headroom for now; revisit only if the
cart actually outgrows a single bank again.

## Configuration

**Compile-time defaults (main.c):**
```c
#define DEFAULT_SERVER_HOST "192.168.2.66"
#define DEFAULT_SERVER_PORT 6465
#define SETTINGS_FILE       "/flash/config/a64browser.cfg"
```

**Runtime settings** (edited via the config screen, persisted to `/flash/config/a64browser.cfg`):
- Server host / IP
- Server port
- Autostart flag (silent connect on boot)

**Settings file format** — three newline-terminated lines:
```
192.168.2.66
6465
1
```
Older single-line files (host only) are accepted for backward compatibility; missing trailing fields keep their `init_state()` defaults.

**Boot flow:**
1. VIC init (cart only) → clear screen, draw "ASSEMBLY64 (LOCAL)"
2. `uci_identify()` — abort with error if Ultimate unreachable
3. `uci_getipaddress()` — cache as `diag_ip` for the config screen
4. `load_settings()` — reads `/flash/config/a64browser.cfg` via DOS_CMD_READ_DATA, draining multi-packet reads into a local buffer (a single `uci_readdata` call exits on the first transient `!isdataavailable` and only returns a partial chunk)
5. If `auto_connect`: silent `connect_to_server()` → on success, jump to PAGE_CATS
6. Otherwise (or on failure): PAGE_SETTINGS with cursor on `.CONNECT.` and the error in the status bar

## Testing

**In VICE emulator:**
```bash
make run  # Launches x64sc with built PRG
```

**On real hardware:**
1. `make deploy` - Upload via FTP
2. `make runprg` - Execute via REST API

## Performance Considerations

- **Minimal memory footprint:** 20 items max per screen
- **No dynamic allocation:** All buffers statically sized
- **Direct hardware access:** VIC-II and UCI registers
- **Line buffering:** Protocol designed for limited memory
- **Server-enforced limits:** No overflow possible

## Known Limitations

1. **Memory:** 32 items per menu buffer (27 A-Z grid cells + room to spare); 20 entries per server-paginated list page
2. **Line length:** 128 bytes max per protocol line (enforced on receive via `uci_tcp_nextline(..., maxlen)`)
3. **Network:** Requires Ultimate II+ or Ultimate 64
4. **Display:** 40 columns x 25 rows (standard C64)
5. **Path depth:** soft-limited by `menu_path[64]` (any path ≥60 chars is truncated)
