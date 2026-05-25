# C64 Protocol Specification

**Current version**: 2.1

## Overview

The C64 protocol is a simple, line-based text protocol designed for low-bandwidth communication between Commodore 64 clients and the `c64uploader` server.
The protocol uses TCP connections with plain text commands and responses, making it easy to implement on resource-constrained 8-bit systems.

## Connection Flow

1. Client establishes TCP connection to server (default port 6465)
2. Server sends greeting: `OK c64uploader\n`
3. Client sends commands, server responds
4. Connection remains open until client sends `QUIT` or timeout occurs (5 minutes of inactivity)
5. Server sends goodbye message and closes connection

## Protocol Characteristics

- **Line-based**: Each command and response line is terminated by `\n` (newline)
- **Text-based**: All data is transmitted as ASCII text
- **Stateless**: Each command is independent
- **Connection timeout**: 5 minutes of inactivity
- **Item limit**: Server sends at most 20 items per paginated response (LIST/LISTPATH/RELEASES/SEARCH). The MENU response can include up to 27 items (the A-Z letter grid); the client buffers up to 32 (MAX_ITEMS).
- **Response terminator**: All multi-line responses end with `.\n`. An empty or invalid MENU returns `OK 0\n.\n` — never a bare `.\n`, which would stall a client waiting for the `OK` header.

## Command Format

Commands are case-insensitive and follow this general format:
```
COMMAND [arg1] [arg2] [arg3]
```

Arguments are separated by whitespace.

## Response Format

All responses start with either `OK` or `ERR`:

### Success Response
```
OK [additional data]\n
[payload lines...]\n
.\n
```

### Error Response
```
ERR [error message]\n
```

The `.` (period) character on its own line indicates the end of multi-line responses.

---

## Command Reference

### 1. MENU - Navigate Menu Hierarchy

The `MENU` command navigates the hierarchical menu structure. It returns menu items with their types, allowing the client to display and navigate through categories, sources, and browse options.

#### Syntax
```
MENU [path]
```

#### Arguments
- `path`: (Optional) Menu path to navigate to. If empty, returns root menu.

#### Response Format
```
OK <count>\n
<type>|<name>|<path>|<count>\n
<type>|<name>|<path>|<count>\n
...
.\n
```

- `count`: Number of items returned (up to 27 for the A-Z letter grid; otherwise at most 20)
- `type`: Item type code:
  - `f` = folder (navigate deeper with MENU)
  - `l` = list (use LISTPATH to get entries)
  - `D` = directory inside MYFILES (navigate deeper with MENU)
  - `F` = file inside MYFILES (execute with RUNFILE, or transfer firmware `.ue2` files with UPLOADFILE)
  - `b` = browse (legacy — older servers emitted this for "Browse A-Z". Current clients still accept it as a LIST, but the server no longer produces `b`. `Browse A-Z` is now a folder.)
- `name`: Display name for the item
- `path`: Full path for this item (used in subsequent commands)
- `count`: Number of entries (for display purposes)

#### Examples

**Example 1: Root menu**

Request:
```
MENU
```

Response:
```
OK 7
f|Games|Games|25000
f|Demos|Demos|15000
f|Music|Music|50000
f|Intros|Intros|3000
f|Graphics|Graphics|1500
f|Discmags|Discmags|500
f|My Files|MYFILES|0
.
```

**Example 2: Games source menu**

Request:
```
MENU Games/CSDB
```

Response:
```
OK 3
f|Browse A-Z|Games/CSDB/A-Z|8500
l|Top 200|Games/CSDB/Top200|200
l|4K Competition|Games/CSDB/4k|450
.
```

Note the `/A-Z` suffix on the Browse A-Z path: it marks the folder that opens the letter grid (see Example 3).

**Example 3: A-Z letter grid**

Any MENU path whose last segment is `A-Z` returns a 27-entry grid menu (A..Z + `#` for non-alphabetic). Each entry is a LIST at `<path>/<letter>`.

Request:
```
MENU Games/CSDB/A-Z
```

Response:
```
OK 27
l|A|Games/CSDB/A-Z/A|523
l|B|Games/CSDB/A-Z/B|412
...
l|Z|Games/CSDB/A-Z/Z|23
l|#|Games/CSDB/A-Z/#|42
.
```

Counts are 0 for letters with no entries; all 27 slots are always returned so clients can lay out a fixed 3x9 grid.

---

### 2. LETTERS - Get Letter Counts for Browse (legacy)

The `LETTERS` command returns available first letters and their entry counts for the letter picker UI. The current client no longer uses this — the A-Z letter grid is delivered via the MENU command at a `/A-Z` path (see MENU Example 3). The server still implements `LETTERS` for older clients.

#### Syntax
```
LETTERS <path>
```

#### Arguments
- `path`: The browse path (from MENU response)

#### Response Format
```
OK <count>\n
<letter>|<count>\n
<letter>|<count>\n
...
.\n
```

- `count`: Number of letters with entries
- `letter`: Single character (A-Z or # for non-alphabetic)
- `count`: Number of entries starting with that letter

#### Example

Request:
```
LETTERS Games/CSDB
```

Response:
```
OK 27
#|42
A|523
B|412
C|389
D|256
E|178
...
Z|23
.
```

---

### 3. LISTPATH - List Entries at Path

The `LISTPATH` command retrieves entries at a specific path with optional letter filtering. Used for browsing entries grouped by title.

#### Syntax
```
LISTPATH <offset> <count> <path> [letter]
```

#### Arguments
- `offset`: Starting index, 0-based
- `count`: Number of entries to return (max 20)
- `path`: Path to list entries from. Two forms are accepted:
  - `Category/Source/<letter>` — legacy letter filter at the source level
  - `Category/Source/A-Z/<letter>` — new A-Z grid leaf, same filtering behavior
- `letter`: (Optional) Filter to entries starting with this letter (A-Z or # for non-alpha). Redundant when the path already includes a letter via either form above.

#### Response Format
```
OK <returned> <total>\n
<id>|<name>|<category>|<release_count>|<trainers>\n
...
.\n
```

- `returned`: Number of entries in this response
- `total`: Total entries matching the query
- `id`: Entry ID (for RUN/INFO commands)
- `name`: Entry title
- `category`: Category abbreviation
- `release_count`: Number of releases for this title
- `trainers`: Trainer count (-1 if unknown)

#### Examples

**Example 1: All entries at path**

Request:
```
LISTPATH 0 20 Games/CSDB
```

Response:
```
OK 20 8500
12345|Arkanoid|Games|3|2
12346|Boulder Dash|Games|5|0
...
.
```

**Example 2: Entries starting with 'A'**

Request:
```
LISTPATH 0 20 Games/CSDB A
```

Response:
```
OK 20 523
12345|Arkanoid|Games|3|2
12350|Ace of Aces|Games|2|0
...
.
```

---

### 4. RELEASES - Get All Releases of a Title

The `RELEASES` command retrieves all variant releases of a specific title. Used when an entry has multiple releases (release_count > 1).

#### Syntax
```
RELEASES <offset> <count> <path> <title>
```

#### Arguments
- `offset`: Starting index, 0-based
- `count`: Number of entries to return (max 20)
- `path`: Category/source path
- `title`: Exact title to match

#### Response Format
```
OK <returned> <total>\n
<id>|<group>|<year>|<type>|<trainers>\n
...
.\n
```

- `id`: Entry ID (for RUN/INFO commands)
- `group`: Release group/publisher
- `year`: Release year
- `type`: File type (prg, d64, crt, etc.)
- `trainers`: Trainer count

#### Example

Request:
```
RELEASES 0 20 Games/CSDB Arkanoid
```

Response:
```
OK 3 3
12345|Imagine|1987|d64|0
12346|Triangle|1988|prg|2
12347|Nostalgia|2010|prg|0
.
```

---

### 5. SEARCH - Search Entries

The `SEARCH` command performs a text search across entry titles and groups.

#### Syntax
```
SEARCH <offset> <count> [category] <query>
```

#### Arguments
- `offset`: Starting index, 0-based
- `count`: Number of results to return (max 20)
- `category`: (Optional) Category filter (Games, Demos, Music)
- `query`: Search term (case-insensitive)

#### Response Format
```
OK <returned> <total>\n
<id>|<name>|<group>|<year>|<type>\n
...
.\n
```

#### Example

Request:
```
SEARCH 0 20 Games ninja
```

Response:
```
OK 5 5
12400|Last Ninja|System 3|1987|d64
12401|Last Ninja 2|System 3|1988|d64
12402|Last Ninja 3|System 3|1991|d64
12403|Ninja|Sculptured|1986|prg
12404|Shadow Ninja|Natsume|1990|d64
.
```

---

### 6. INFO - Get Entry Details

The `INFO` command retrieves detailed metadata for a specific entry.

#### Syntax
```
INFO <id>
```

#### Arguments
- `id`: Entry ID (from LISTPATH, RELEASES, or SEARCH)

#### Response Format
```
OK\n
NAME|<name>\n
GROUP|<group>\n
YEAR|<year>\n
TYPE|<file_type>\n
REL|<release_name>\n
AUTHOR|<author>\n
TOP200|#<rank>\n
RATING|<rating>\n
.\n
```

All fields after NAME/GROUP/TYPE are optional — the server omits them when the underlying value is empty or NULL. Clients should read label-prefixed lines until `.\n` rather than expecting a fixed order or set.

#### Example

Request:
```
INFO 12400
```

Response:
```
OK
NAME|Last Ninja
GROUP|System 3
YEAR|1987
TYPE|d64
REL|Last Ninja +D
TOP200|#85
RATING|8.3
.
```

---

### 7. RUN - Execute Entry

The `RUN` command uploads and executes an entry on the C64 Ultimate hardware.

#### Syntax
```
RUN <id>
```

#### Arguments
- `id`: Entry ID

#### Response Format
```
OK Running <entry_name>\n
```

or

```
ERR <error_message>\n
```

#### Behavior by File Type

- **PRG files**: Loaded into memory and executed
- **CRT files**: Cartridge image is mounted
- **SID files**: Music is played via Ultimate's SID player
- **D64/G64/D71/D81**: Disk image mounted, first program loaded and run

---

### 8. RUNFILE - Execute File from My Files

The `RUNFILE` command uploads and executes a file from the user's personal `myfiles/` directory.

#### Syntax
```
RUNFILE MYFILES/<path>
```

#### Arguments
- `path`: Path to file within myfiles directory (e.g., `MYFILES/games/mygame.prg`)

#### Response Format
```
OK Running\n
```

or

```
ERR <error_message>\n
```

#### Errors
- `ERR Usage: RUNFILE <path>` - Missing path argument
- `ERR Invalid MYFILES path` - Path doesn't start with MYFILES/
- `ERR Invalid path` - Path traversal attempt or invalid path
- `ERR Invalid file type` - File extension not supported
- `ERR Use UPLOADFILE for firmware files` - File is a `.ue2` firmware/flash-patch (upload-only); use UPLOADFILE instead
- `ERR File not found` - File doesn't exist
- `ERR Failed to run: <reason>` - Ultimate API error

#### Supported File Types
Same as RUN: .prg, .crt, .sid, .d64, .g64, .d71, .d81, .t64, .tap
(`.ue2` firmware files appear in MYFILES but are upload-only — use UPLOADFILE.)

#### Example

Request:
```
RUNFILE MYFILES/games/mygame.prg
```

Response:
```
OK Running
```

---

### 9. UPLOADFILE - Transfer File to Device (no launch)

The `UPLOADFILE` command transfers a file from the user's personal `myfiles/`
directory to the device's `/Temp` directory via FTP **without launching it**. It
exists for Ultimate firmware/flash-patch files (`.ue2`): there is no REST flash
endpoint, so the file is only staged on the device and the user applies the patch
from the Ultimate's on-device menu.

#### Syntax
```
UPLOADFILE MYFILES/<path>
```

#### Arguments
- `path`: Path to file within myfiles directory (e.g., `MYFILES/firmware/Prkl_FlashPatch_1.1.0s2p7.ue2`)

#### Response Format
```
OK Uploaded /Temp/<filename>\n
```

or

```
ERR <error_message>\n
```

The original filename is preserved on the device.

#### Errors
- `ERR Usage: UPLOADFILE <path>` - Missing path argument
- `ERR Invalid MYFILES path` - Path doesn't start with MYFILES/
- `ERR Invalid path` - Path traversal attempt or invalid path
- `ERR Invalid file type` - File extension not supported
- `ERR File not found` - File doesn't exist
- `ERR Failed to upload: <reason>` - FTP error

#### Example

Request:
```
UPLOADFILE MYFILES/firmware/Prkl_FlashPatch_1.1.0s2p7.ue2
```

Response:
```
OK Uploaded /Temp/Prkl_FlashPatch_1.1.0s2p7.ue2
```

---

### 10. QUIT - Close Connection

The `QUIT` command closes the connection gracefully.

#### Syntax
```
QUIT
```

#### Response
```
OK Goodbye\n
```

Server closes connection after sending this response.

---

## Navigation Flow

The typical navigation flow for the C64 client:

1. **Root Menu**: `MENU` -> displays categories (Games, Demos, Music, etc. + My Files)
2. **Category Menu**: `MENU Games` -> displays sources (CSDB, Gamebase, etc.)
3. **Source Menu**: `MENU Games/CSDB` -> displays navigation options; one of them is the `f|Browse A-Z|.../A-Z|N` folder
4. **Letter Grid**: `MENU Games/CSDB/A-Z` -> returns 27 `l` entries the client renders as a 3x9 grid
5. **Entry List**: `LISTPATH 0 20 Games/CSDB/A-Z/A` -> displays entries starting with 'A'
6. **Releases**: `RELEASES 0 20 Games/CSDB "Arkanoid"` -> shows all versions
7. **Run**: `RUN 12345` -> executes the selected entry

## Memory Constraints

The C64 client buffers up to 32 menu items (MAX_ITEMS = 32). Server response limits:
- MENU responses: up to 27 items (the A-Z letter grid). All other menus return at most ~10 items in practice.
- LETTERS responses (legacy): up to 27 items (A-Z + #)
- LISTPATH/RELEASES/SEARCH responses: max 20 items per page

Pagination is handled client-side using offset/count parameters.
