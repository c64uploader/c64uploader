/*****************************************************************
 * Assembly64 Browser - C64 Client
 *
 * Native C64 client for browsing Assembly64 database via
 * Ultimate II+ network interface.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Statically links the GPL v3 ultimate.{c,h} UCI library, so the
 * resulting binary is GPL v3. See ../LICENSE for the full text.
 *****************************************************************/

#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <c64/vic.h>
#include <c64/keyboard.h>
#include "ultimate.h"

// EasyFlash (`-tf=crt`) layout. oscar64's default `main` region is
// 0x0900-0x4700 (~16 KB) — same as PRG — so a 17 KB binary can't be
// placed and the link fails with `Could not place object` errors and
// "Cannot place stack/heap section". Stretch main to all of low RAM
// (0x0900-0x8000, ~30 KB) following the easyflashshared sample. The
// boot stub LZ-compresses this region into cart bank 0 ROM and
// decompresses it back into RAM at startup, so code/data/BSS/heap/
// stack all live in RAM after boot — same shape as PRG, just delivered
// through a cart. The PRG build uses oscar64's auto-configured region
// and ignores this pragma.
//
// Important Ultimate caveat: builds with `-tf=crt` *must* also pass
// `-csub=1` (REU-aware EasyFlash subtype 1). Subtype 0 makes the
// Ultimate firmware claim all of $DF00-$DFFF for emulated cart RAM,
// hides the UCI registers at $DF1C-$DF1F, and uci_identify() hangs
// forever. With subtype 1 the firmware *relocates* UCI to $DE1C-$DE1F
// (I/O 1) — not unmaps it; the registers move. ultimate.h picks the
// right addresses based on OSCAR_TARGET_CRT_EASYFLASH.
#ifdef OSCAR_TARGET_CRT_EASYFLASH
#pragma region(main, 0x0900, 0x8000, , , {code, data, bss, heap, stack})
#endif

// Server configuration
#define DEFAULT_SERVER_HOST "192.168.2.66"
#define DEFAULT_SERVER_PORT 6465
#define SETTINGS_DIR  "/flash/config"
#define SETTINGS_FILE "/flash/config/a64browser.cfg"

// Settings structure. Defaults are set at runtime by init_state() so the
// vars live in writable BSS — under cart targets, statics with explicit
// initializers end up in cart ROM and become read-only.
static char server_host[32];
static int  server_port;
static bool auto_connect;

// Diagnostic strings captured once at boot from uci_identify and
// uci_getipaddress. The config screen shows them at the top so they're
// visible whether we got there via auto-connect failure or via F1 from
// the main menu (without needing to re-poll the device).
static char diag_ultimate[24];
static char diag_ip[20];

// Screen dimensions
#define SCREEN_WIDTH  40
#define SCREEN_HEIGHT 25
#define LIST_HEIGHT   18  // Lines available for list display

// UI state
static byte socket_id;
static bool connected;

// Pages: 0=cats, 1=list, 2=search, 3=settings, 6=info, 7=releases.
// Page 4 / 5 (advanced search) are gone — replaced by the simple typed-query
// search at PAGE_SEARCH that drives the existing server SEARCH command.
#define PAGE_CATS        0
#define PAGE_LIST        1
#define PAGE_SEARCH      2
#define PAGE_SETTINGS    3
#define PAGE_INFO        6
#define PAGE_RELEASES    7

// Navigation uses path trimming - no stack needed
// The menu path hierarchy IS the navigation history:
//   "" -> "Games" -> "Games/CSDB" -> entries
// Going back = trim last path component

// Menu/list state. 27 must fit (A-Z + '#' letter grid); server still pages
// regular lists at 20 entries per page, so headroom here is cheap.
#define MAX_ITEMS 32
static char item_names[MAX_ITEMS][32];
static long item_ids[MAX_ITEMS];
static int  item_count;
static long total_count;
static int  cursor;
static int  offset;
static int  current_page;  // PAGE_CATS == 0; BSS zero-init is fine.

// Current category
static char current_category[48];  // Path for LISTPATH/RELEASES

// Menu navigation state
static char menu_path[64];              // Current path like "Games/CSDB/All"
static char menu_paths[MAX_ITEMS][48];  // Path for each menu item
static char menu_types[MAX_ITEMS];      // 'f'=folder, 'l'=list

// Forward declarations for navigation functions
void go_back(void);
void draw_list(const char *title);
void draw_releases(void);
void do_releases(const char *category, const char *title, int start);
void run_myfile(const char *path);
static bool is_letter_grid(void);
void update_letter_grid_cursor(int old_cursor, int new_cursor);
static void draw_search_input(void);

// Simple search state. Server SEARCH command takes "<offset> <count>
// [category] <query>" and returns id|name|group|year|type rows. Tab (C= key
// in PETSCII, scancode 0x0F) cycles search_category through these labels.
static const char *search_cat_names[] = {"All", "Games", "Demos", "Music"};

static char search_query[24];
static byte search_query_len;
static byte search_category;
// Two-mode search: cursor lives either in the input box (typing) or in the
// result list (navigating). ENTER or cursor-down in the box runs the query
// and switches modes; cursor-up at the top of the list (or DEL) returns to
// the box. Typing only goes to the query while in the box, so held letters
// can't ever corrupt query state by leaking into the result-nav path.
#define SEARCH_MIN_QUERY_LEN 3
static bool search_in_box;  // Set to true by init_state() for cart safety.

// Settings edit state. The config page (former PAGE_SETTINGS) has 5 rows:
// SERVER (text), PORT (decimal int), AUTOCONN (toggle), CONNECT (action),
// SAVE (action). settings_cursor is 0..CFG_FIELD_COUNT-1.
#define CFG_FIELD_SERVER   0
#define CFG_FIELD_PORT     1
#define CFG_FIELD_AUTOCONN 2
#define CFG_FIELD_CONNECT  3
#define CFG_FIELD_SAVE     4
#define CFG_FIELD_COUNT    5
static int  settings_cursor;
static int  settings_edit_pos;
static bool settings_editing;
static char port_edit_buf[8];  // Decimal-string scratch when editing PORT.

// Line buffer for protocol
static char line_buffer[128];

// Info screen state
static int info_return_page;  // PAGE_CATS == 0
#define MAX_INFO_LINES 12
static char info_labels[MAX_INFO_LINES][8];   // "NAME", "GROUP", etc.
static char info_values[MAX_INFO_LINES][32];  // The values
static int  info_line_count;

// Grouped results state (for advanced search)
static int  item_counts[MAX_ITEMS];    // Release count per grouped entry
static char item_cats[MAX_ITEMS][8];   // Category per grouped entry
static int  item_trainers[MAX_ITEMS];  // Trainer count per entry (-1=unknown, 0=none, >0=count)

// Releases page state
static char releases_title[32];        // Title being viewed
static char releases_category[48];     // Path or category of releases
static int  releases_return_page;  // Set to PAGE_LIST by init_state().
static int  releases_return_offset;
static int  releases_return_cursor;

// When entering a LIST from a menu (including the A-Z letter grid), remember
// where the cursor was so go_back can put it back rather than snap to item 0.
static int  menu_return_cursor;

// VIC chip at $D000
#define vic (*(struct VIC *)0xd000)

// Screen memory
#define SCREEN_RAM ((char*)0x0400)
#define COLOR_RAM  ((char*)0xD800)

//-----------------------------------------------------------------------------
// Screen utilities
//-----------------------------------------------------------------------------

void clear_screen(void)
{
    memset(SCREEN_RAM, ' ', 1000);
    memset(COLOR_RAM, 14, 1000);  // Light blue
}

void print_at(byte x, byte y, const char *text)
{
    char *pos = SCREEN_RAM + y * 40 + x;
    while (*text)
    {
        char c = *text++;
        // Convert ASCII to PETSCII screen codes
        if (c >= 'a' && c <= 'z')
            c = c - 'a' + 1;
        else if (c >= 'A' && c <= 'Z')
            c = c - 'A' + 1;
        else if (c == '|')
            c = ' ';  // Replace pipe separator with space
        *pos++ = c;
    }
}

void print_at_color(byte x, byte y, const char *text, byte color)
{
    char *pos = SCREEN_RAM + y * 40 + x;
    char *col = COLOR_RAM + y * 40 + x;
    while (*text)
    {
        char c = *text++;
        if (c >= 'a' && c <= 'z')
            c = c - 'a' + 1;
        else if (c >= 'A' && c <= 'Z')
            c = c - 'A' + 1;
        else if (c == '|')
            c = ' ';
        *pos++ = c;
        *col++ = color;
    }
}

void clear_line(byte y)
{
    memset(SCREEN_RAM + y * 40, ' ', 40);
}

void print_status(const char *msg)
{
    clear_line(24);
    print_at(0, 24, msg);
}

//-----------------------------------------------------------------------------
// Settings
//-----------------------------------------------------------------------------

// Settings file format: three newline-terminated lines.
//   line 1: server hostname or IP
//   line 2: server port (decimal)
//   line 3: auto-connect flag (0 or 1)
// Older single-line files (server only) are accepted; missing trailing
// fields keep their init_state defaults.
void load_settings(void)
{
    uci_settarget(UCI_TARGET_DOS1);

    uci_open_file(0x01, SETTINGS_FILE);
    if (!uci_success())
        return;

    // Drain the read response into a local buffer. uci_readdata exits on
    // the first transient !isdataavailable, so a single call can return a
    // partial chunk; loop until we either fill the buffer or stop seeing
    // new bytes after a wait. Without this drain the tail of the file
    // bleeds into the next UCI call's response.
    char buf[128];
    int total = 0;
    uci_read_file(127);
    while (total < (int)sizeof(buf) - 1)
    {
        int timeout = 2000;
        while (!uci_isdataavailable() && timeout > 0)
            timeout--;
        if (!uci_isdataavailable())
            break;
        int n = uci_readdata();
        if (n <= 0)
            break;
        if (total + n > (int)sizeof(buf) - 1)
            n = sizeof(buf) - 1 - total;
        memcpy(buf + total, uci_data, n);
        total += n;
    }
    buf[total] = 0;

    uci_readstatus();
    uci_accept();
    uci_close_file();

    if (total <= 0)
        return;

    int len = total;
    // Walk through up to 3 lines.
    int i = 0;
    int field = 0;
    while (i < len && field < 3)
    {
        int start = i;
        while (i < len && buf[i] != '\n' && buf[i] != '\r' && buf[i] != 0)
            i++;
        int line_len = i - start;
        while (i < len && (buf[i] == '\n' || buf[i] == '\r'))
            i++;

        if (line_len > 0)
        {
            char line[32];
            int n = line_len < 31 ? line_len : 31;
            memcpy(line, &buf[start], n);
            line[n] = 0;

            switch (field)
            {
            case 0:
                strcpy(server_host, line);
                break;
            case 1:
                {
                    int p = atoi(line);
                    if (p > 0 && p < 65536)
                        server_port = p;
                }
                break;
            case 2:
                auto_connect = (line[0] == '1');
                break;
            }
        }
        field++;
    }
}

void save_settings(void)
{
    uci_settarget(UCI_TARGET_DOS1);

    // /flash/config matches the prkl/Spiffy convention; create_dir is a
    // no-op if it already exists.
    uci_create_dir(SETTINGS_DIR);

    uci_delete_file(SETTINGS_FILE);

    uci_open_file(0x06, SETTINGS_FILE);  // 0x06 = create + write
    if (!uci_success())
        return;

    char line[40];
    sprintf(line, "%s\n%d\n%d\n",
            server_host, server_port, auto_connect ? 1 : 0);
    uci_write_file((uint8_t*)line, strlen(line));
    uci_close_file();
}

//-----------------------------------------------------------------------------
// Network
//-----------------------------------------------------------------------------

bool connect_to_server(void)
{
    // Close any prior socket. Even after the server dies on its end,
    // the Ultimate firmware still considers the old socket open until
    // we explicitly close it — and may reject the next uci_tcp_connect
    // because resources are still bound. Without this cleanup the user
    // sees "connect failed" on every retry until they reboot the C64.
    if (connected)
    {
        uci_socket_close(socket_id);
        connected = false;
    }
    uci_reset_data();

    print_status("connecting...");

    socket_id = uci_tcp_connect(server_host, server_port);

    // socket_id == 0 catches the firmware-reports-success-but-no-socket
    // edge case where uci_status is "00" yet no actual TCP connection
    // was made (rare, but cheap to guard against).
    if (!uci_success() || socket_id == 0)
    {
        print_status("connect failed - check server address");
        return false;
    }

    // Read greeting line "OK c64uploader". If the TCP socket is open
    // but the server never sends bytes (wrong port, server crashed,
    // firewall sinkhole), uci_tcp_nextline returns false after its
    // internal retry budget — without this check we would silently
    // proceed into the menu and hang on the first real request.
    if (!uci_tcp_nextline(socket_id, line_buffer, sizeof(line_buffer)))
    {
        uci_socket_close(socket_id);
        print_status("no response from server");
        return false;
    }

    connected = true;
    print_status("connected!");
    return true;
}

void disconnect_from_server(void)
{
    if (connected)
    {
        uci_socket_write(socket_id, "QUIT\n");
        uci_socket_close(socket_id);
        connected = false;
    }
}

//-----------------------------------------------------------------------------
// Protocol
//-----------------------------------------------------------------------------

// Forward declarations
void load_list_path(const char *path, int start);

// Check if server response is an error
static bool is_error_response(void)
{
    return line_buffer[0] == 'E' && line_buffer[1] == 'R';
}

void send_command(const char *cmd)
{
    if (!connected)
        return;
    uci_socket_write(socket_id, cmd);
    uci_socket_write_char(socket_id, '\n');
}

// Read a line from server, returns length
int read_line(void)
{
    return uci_tcp_nextline(socket_id, line_buffer, sizeof(line_buffer));
}

// Parse "OK n total" response, returns n
int parse_ok_count(void)
{
    // line_buffer contains "OK n total"
    char *p = line_buffer + 3;  // Skip "OK "
    return atoi(p);
}

// Load menu from server and display it
// Sets menu_path, item_names, menu_paths, menu_types, item_count
void load_menu(const char *path)
{
    // Callers typically pass menu_paths[cursor] as `path`, and the parser
    // loop below overwrites menu_paths[] with the new server response. Snapshot
    // into a local buffer so the final strncpy into menu_path survives.
    char saved_path[64];
    strncpy(saved_path, path, 63);
    saved_path[63] = 0;

    print_status("loading...");

    // Build MENU command
    char cmd[80];
    if (saved_path[0])
        sprintf(cmd, "MENU %s", saved_path);
    else
        strcpy(cmd, "MENU");

    send_command(cmd);
    read_line();  // "OK n"

    if (is_error_response())
    {
        item_count = 0;
        total_count = 0;
        print_status(line_buffer);
        return;
    }

    item_count = 0;
    total_count = parse_ok_count();

    // Read menu lines until "."
    // Format: type|name|path|count
    while (item_count < MAX_ITEMS)
    {
        read_line();
        if (line_buffer[0] == '.')
            break;

        // Parse "type|name|path|count"
        char *type = line_buffer;
        char *name = strchr(type, '|');
        if (!name) continue;
        *name++ = 0;

        char *mpath = strchr(name, '|');
        if (!mpath) continue;
        *mpath++ = 0;

        char *count = strchr(mpath, '|');
        if (!count) continue;
        *count++ = 0;

        strncpy(item_names[item_count], name, 31);
        item_names[item_count][31] = 0;
        strncpy(menu_paths[item_count], mpath, 47);
        menu_paths[item_count][47] = 0;
        menu_types[item_count] = type[0];  // 'f' or 'l'
        item_ids[item_count] = atol(count);  // Store count
        item_count++;
    }

    // Save current path (from our snapshot, since menu_paths[] was rewritten)
    strncpy(menu_path, saved_path, 63);
    menu_path[63] = 0;

    cursor = 0;
    offset = 0;
    current_page = PAGE_CATS;
    // In a letter grid, item_ids[] holds per-letter counts; skip past any
    // leading empty letter so the cursor never lands on a non-tappable cell.
    if (is_letter_grid())
    {
        while (cursor < item_count && item_ids[cursor] == 0)
            cursor++;
        if (cursor >= item_count) cursor = 0;
    }
    print_status("ready");
}

// Trim last path component: "Games/CSDB" -> "Games", "Games" -> ""
static void trim_path(char *path)
{
    char *last = strrchr(path, '/');
    if (last)
        *last = 0;
    else
        path[0] = 0;
}

// Navigate back - uses path hierarchy instead of a stack
// Menu paths are hierarchical: "" -> "Games" -> "Games/CSDB"
// Going back = trim last component and reload
void go_back(void)
{
    if (current_page == PAGE_RELEASES)
    {
        // Releases -> back to entry list. Restore both the page offset and
        // the cursor position so the user lands on the same row they entered
        // releases from (load_list_path resets cursor to 0, so set it after).
        load_list_path(current_category, releases_return_offset);
        if (releases_return_cursor < item_count)
            cursor = releases_return_cursor;
        current_page = PAGE_LIST;
        draw_list(current_category);
    }
    else if (current_page == PAGE_LIST)
    {
        // Entry list -> back to parent menu. menu_path was set by the last
        // successful load_menu (the parent we came from), so just reload it.
        // Works whether the list lives directly under that menu (old Browse
        // A-Z) or one level deeper (new A-Z/letter layout), and avoids the
        // invalid "MENU Category/Source/A-Z/K" call that would stall the
        // client because the server has no menu at that leaf path.
        load_menu(menu_path);
        if (menu_return_cursor < item_count)
            cursor = menu_return_cursor;
        draw_list(menu_path[0] ? menu_path : "assembly64 - categories");
    }
    else if (current_page == PAGE_CATS)
    {
        // Menu -> back to parent menu (trim path)
        if (menu_path[0] == 0)
            return;  // Already at root, can't go back

        trim_path(menu_path);
        load_menu(menu_path);
        draw_list(menu_path[0] ? menu_path : "assembly64 - categories");
    }
}

// Reset to root menu
void load_categories(void)
{
    load_menu("");
}

// Load entries by path using LISTPATH command
void load_list_path(const char *path, int start)
{
    print_status("loading...");

    char cmd[96];
    sprintf(cmd, "LISTPATH %d 20 %s", start, path);
    send_command(cmd);
    read_line();  // "OK n total"

    if (is_error_response())
    {
        item_count = 0;
        total_count = 0;
        cursor = 0;
        print_status(line_buffer);
        return;
    }

    // Parse "OK n total"
    char *p = line_buffer + 3;
    int n = atoi(p);
    p = strchr(p, ' ');
    if (p)
        total_count = atol(p + 1);

    item_count = 0;
    offset = start;

    // Read entry lines until "."
    // Format: ID|Name|Category|Count|Trainers (grouped by title)
    while (item_count < MAX_ITEMS && item_count < n)
    {
        read_line();
        if (line_buffer[0] == '.')
            break;

        char *id_str = line_buffer;
        char *name = strchr(id_str, '|');
        if (!name) continue;
        *name++ = 0;

        char *cat = strchr(name, '|');
        if (!cat) continue;
        *cat++ = 0;

        char *count = strchr(cat, '|');
        if (!count) continue;
        *count++ = 0;

        char *trainers = strchr(count, '|');
        if (trainers) *trainers++ = 0;

        item_ids[item_count] = atol(id_str);
        strncpy(item_names[item_count], name, 31);
        item_names[item_count][31] = 0;
        strncpy(item_cats[item_count], cat, 7);
        item_cats[item_count][7] = 0;
        item_counts[item_count] = atoi(count);  // Number of releases
        item_trainers[item_count] = trainers ? atoi(trainers) : -1;
        item_count++;
    }

    cursor = 0;
    print_status("ready");
}

// Run selected entry
void run_entry(long id)
{
    print_status("running...");

    char cmd[32];
    sprintf(cmd, "RUN %ld", id);
    send_command(cmd);

    read_line();  // "OK Running xxx" or "ERR xxx"
    print_status(line_buffer);
}

// Run file from MYFILES directory
void run_myfile(const char *path)
{
    print_status("running...");

    char cmd[64];
    sprintf(cmd, "RUNFILE %s", path);
    send_command(cmd);

    read_line();  // "OK Running xxx" or "ERR xxx"
    print_status(line_buffer);
}

// True if name ends in ".ue2" (case-insensitive). Ultimate firmware/flash-patch
// files are transferred to the device but never launched.
char is_ue2(const char *name)
{
    int len = strlen(name);
    if (len < 4)
        return 0;
    const char *e = name + len - 4;
    return e[0] == '.'
        && (e[1] == 'u' || e[1] == 'U')
        && (e[2] == 'e' || e[2] == 'E')
        && e[3] == '2';
}

// Upload file from MYFILES to the device via FTP without launching it (firmware).
void upload_myfile(const char *path)
{
    print_status("uploading...");

    char cmd[64];
    sprintf(cmd, "UPLOADFILE %s", path);
    send_command(cmd);

    read_line();  // "OK Uploaded /Temp/xxx" or "ERR xxx"
    print_status(line_buffer);
}

// Open a selected MYFILES file: firmware (.ue2) is uploaded only, anything else is run.
void open_myfile(const char *path)
{
    if (is_ue2(path))
        upload_myfile(path);
    else
        run_myfile(path);
}

// Drive the existing server SEARCH command. Response is one row per result,
// "id|name|group|year|type" — we only render id+name; the rest is dropped on
// the floor. category=0 ("All") sends no category arg, so the server runs
// across all categories.
void do_search(const char *query, int start)
{
    char cmd[64];
    if (search_category == 0)
        sprintf(cmd, "SEARCH %d 20 %s", start, query);
    else
        sprintf(cmd, "SEARCH %d 20 %s %s", start, search_cat_names[search_category], query);
    send_command(cmd);
    read_line();
    if (is_error_response()) {
        item_count = 0;
        total_count = 0;
        cursor = 0;
        print_status(line_buffer);
        return;
    }
    char *p = line_buffer + 3;
    int n = atoi(p);
    p = strchr(p, ' ');
    if (p)
        total_count = atol(p + 1);
    item_count = 0;
    offset = start;
    while (item_count < MAX_ITEMS && item_count < n) {
        read_line();
        if (line_buffer[0] == '.')
            break;
        char *id_str = line_buffer;
        char *name = strchr(id_str, '|');
        if (!name)
            continue;
        *name++ = 0;
        item_ids[item_count] = atol(id_str);
        char *end = strchr(name, '|');
        if (end)
            *end = 0;
        strncpy(item_names[item_count], name, 31);
        item_names[item_count][31] = 0;
        item_count++;
    }
    {
        int safety = 100;
        while (line_buffer[0] != '.' && safety-- > 0)
            read_line();
    }
    cursor = 0;
}

// Fetch releases for a specific title
void do_releases(const char *category, const char *title, int start)
{
    print_status("loading releases...");

    // Save title and category/path for display
    strncpy(releases_title, title, 31);
    releases_title[31] = 0;
    strncpy(releases_category, category, 47);
    releases_category[47] = 0;

    // Build command: "RELEASES offset count category title"
    char cmd[128];
    sprintf(cmd, "RELEASES %d 20 %s %s", start, category, title);

    send_command(cmd);
    read_line();  // "OK n total"

    if (is_error_response())
    {
        item_count = 0;
        total_count = 0;
        cursor = 0;
        print_status(line_buffer);
        return;
    }

    // Parse "OK n total"
    char *p = line_buffer + 3;
    int n = atoi(p);
    p = strchr(p, ' ');
    if (p)
        total_count = atol(p + 1);

    item_count = 0;
    offset = start;

    // Read entry lines until "."
    // Releases format: "id|group|year|type|trainers"
    while (item_count < MAX_ITEMS && item_count < n)
    {
        read_line();
        if (line_buffer[0] == '.')
            break;

        // Parse "id|group|year|type|trainers"
        char *id_str = line_buffer;
        char *grp = strchr(id_str, '|');
        if (grp)
        {
            *grp++ = 0;
            item_ids[item_count] = atol(id_str);

            // Find year (next |)
            char *year = strchr(grp, '|');
            if (year)
            {
                *year++ = 0;
                // Find type (next |)
                char *type = strchr(year, '|');
                if (type)
                {
                    *type++ = 0;
                    // Find trainers (next |)
                    char *trn = strchr(type, '|');
                    if (trn)
                    {
                        *trn++ = 0;
                        item_trainers[item_count] = atoi(trn);
                    }
                    else
                    {
                        item_trainers[item_count] = -1;
                    }
                }
                else
                {
                    item_trainers[item_count] = -1;
                }
            }
            else
            {
                item_trainers[item_count] = -1;
            }

            // Display group name
            strncpy(item_names[item_count], grp, 31);
            item_names[item_count][31] = 0;
            item_count++;
        }
    }

    // Consume remaining lines until "."
    {
        int safety = 100;
        while (line_buffer[0] != '.' && safety-- > 0)
            read_line();
    }

    cursor = 0;
    print_status("ready");
}

// Fetch info for an entry
bool fetch_info(long id)
{
    print_status("loading info...");

    char cmd[32];
    sprintf(cmd, "INFO %ld", id);
    send_command(cmd);
    read_line();  // "OK" or "ERR ..."

    if (line_buffer[0] == 'E')
    {
        print_status(line_buffer);
        return false;
    }

    info_line_count = 0;

    // Read field lines until "."
    while (info_line_count < MAX_INFO_LINES)
    {
        read_line();
        if (line_buffer[0] == '.')
            break;

        // Parse "LABEL|value"
        char *sep = strchr(line_buffer, '|');
        if (sep)
        {
            *sep = 0;
            // Only add if value is non-empty
            if (sep[1] != 0)
            {
                strncpy(info_labels[info_line_count], line_buffer, 7);
                info_labels[info_line_count][7] = 0;
                strncpy(info_values[info_line_count], sep + 1, 31);
                info_values[info_line_count][31] = 0;
                info_line_count++;
            }
        }
    }

    // Consume any remaining lines
    {
        int safety = 100;
        while (line_buffer[0] != '.' && safety-- > 0)
            read_line();
    }

    print_status("ready");
    return info_line_count > 0;
}

//-----------------------------------------------------------------------------
// Keyboard input
//-----------------------------------------------------------------------------

// Debug injection: uploader writes a semantic key code into $02A7 (reserved
// unused KERNAL scratch byte) and get_key() consumes it as if the user pressed
// a key. Non-zero value is returned verbatim and then cleared.
#define DEBUG_KEY_INJECT (*(volatile byte *)0x02A7)

// Debug hold-key: if $02A8 is non-zero, pretend that scancode (low 6 bits,
// bit 6 = shift qualifier) is being held on the keyboard matrix. Lets the
// uploader measure auto-repeat timing without a physical key press.
#define DEBUG_HOLD_SCAN (*(volatile byte *)0x02A8)

// Press-then-auto-repeat state. The KERNAL jiffy clock at $A2 only ticks
// under the .prg target (KERNAL IRQ runs there). Cart targets bypass KERNAL
// boot, so $A2 is frozen and any timer comparison against it stays stuck in
// the initial-delay window forever — auto-repeat doesn't fire. We tick our
// own jiffy off the VIC raster register ($D012) instead: it advances 50/60×
// per second regardless of IRQ state. tick_jiffy() must be called more often
// than once per frame for this to be precise; in practice the main loop
// polls get_key() far faster.
#define VIC_RASTER (*(volatile byte *)0xD012)
static byte sw_jiffy;
static byte last_raster;
static inline void tick_jiffy(void)
{
    byte r = VIC_RASTER;
    // Raster counts 0..(255 or 311) and wraps; on PAL $D012 wraps every
    // 256 lines (the 9th bit lives in $D011), so we just count strict
    // decreases as "frame transition".
    if (r < last_raster)
        ++sw_jiffy;
    last_raster = r;
}
#define KEY_INITIAL_DELAY 18  // ~360ms between fresh press and first auto-repeat
#define KEY_REPEAT_RATE    3  // ~60ms between repeats (~16 Hz)

// Initialized to 0xFF by init_state() — under cart targets a static `= 0xFF`
// would land in read-only ROM and the auto-repeat tracker would never reset.
static byte last_key_scan;
static byte next_fire_jiffy;
// Consecutive-poll counter that gates last_key_scan release. The C64 matrix
// occasionally reads "no key" for a single poll while the user is still
// holding (electrical noise / scanner timing — see comment below). Only
// declare release after the matrix is quiet for several polls in a row,
// otherwise the brief glitch makes the next re-detection look like a
// fresh press and held keys (esp. SPACE) fire as repeats.
static byte release_count;
#define RELEASE_STABLE_POLLS 3

char get_key(void)
{
    tick_jiffy();

    byte injected = DEBUG_KEY_INJECT;
    if (injected)
    {
        DEBUG_KEY_INJECT = 0;
        // Drop physical tracking so a held key after injection doesn't
        // immediately auto-repeat on the same scancode.
        last_key_scan = 0xFF;
        return (char)injected;
    }

    byte k;
    bool shift;
    byte hold = DEBUG_HOLD_SCAN;
    if (hold)
    {
        // Simulated held key: skip matrix poll entirely.
        k = hold & 0x3f;
        shift = (hold & 0x40) != 0;

        byte now = sw_jiffy;
        if (k != last_key_scan)
        {
            last_key_scan = k;
            next_fire_jiffy = now + KEY_INITIAL_DELAY;
        }
        else
        {
            if ((byte)(now - next_fire_jiffy) >= 128)
                return 0;
            next_fire_jiffy = now + KEY_REPEAT_RATE;
        }
    }
    else
    {
        keyb_poll();

        // Oscar64's keyb_poll() is an edge detector: keyb_key is only non-zero
        // on the frame a key transitions from up to down. For held-key auto-
        // repeat we have to check keyb_matrix (level state) via key_pressed().
        byte now = sw_jiffy;

        // Two paths can produce a held-state repeat: the "still pressed on
        // the matrix" branch below, and a spurious one from keyb_poll —
        // when the C64's keyboard scanner briefly reads "no key" for a
        // single poll (timing/electrical noise) it forgets the prior matrix
        // state, so the next poll re-fires the same scancode as a fresh
        // edge. Both paths get the same auto-repeat filter: only W / S /
        // cursor-down / cursor-right are allowed to repeat.
        bool nav_repeat_ok =
            last_key_scan == KSCAN_W || last_key_scan == KSCAN_S ||
            last_key_scan == KSCAN_CSR_DOWN || last_key_scan == KSCAN_CSR_RIGHT;

        if (keyb_key & KSCAN_QUAL_DOWN)
        {
            byte new_k = keyb_key & 0x3f;
            release_count = 0;
            // Spurious-edge re-fire: same scancode as last time, and level
            // state confirms the user has held the key continuously. Treat
            // as a held repeat, NOT a fresh press.
            if (new_k == last_key_scan && key_pressed(new_k))
            {
                if (!nav_repeat_ok)
                    return 0;
                if ((byte)(now - next_fire_jiffy) >= 128)
                    return 0;
                next_fire_jiffy = now + KEY_REPEAT_RATE;
                k = last_key_scan;
                shift = key_shift();
            }
            else
            {
                // Genuine fresh press.
                k = new_k;
                shift = (keyb_key & KSCAN_QUAL_SHIFT) != 0;
                last_key_scan = k;
                next_fire_jiffy = now + KEY_INITIAL_DELAY;
            }
        }
        else if (last_key_scan != 0xFF && key_pressed(last_key_scan))
        {
            // Same scan code still held on the matrix.
            release_count = 0;
            if (!nav_repeat_ok)
                return 0;
            if ((byte)(now - next_fire_jiffy) >= 128)
                return 0;
            next_fire_jiffy = now + KEY_REPEAT_RATE;
            k = last_key_scan;
            shift = key_shift();
        }
        else
        {
            // No key on this poll. Could be a genuine release, OR a brief
            // matrix-read glitch while the key is still physically held.
            //
            // For typing keys (everything except nav), debounce: only drop
            // tracking after the quiet has held for several polls, so a
            // 1-poll glitch doesn't masquerade as release-then-repress
            // (which is what made SPACE fire repeatedly).
            //
            // For nav keys (W / S / cursor-down / cursor-right), drop
            // immediately like the original code — they're explicitly
            // allowed to auto-repeat, and debouncing the release here makes
            // the matrix-glitch case stall the held-branch's fire ticks,
            // which the user perceives as "scroll keys don't repeat".
            if (last_key_scan != 0xFF && !nav_repeat_ok &&
                release_count < RELEASE_STABLE_POLLS)
            {
                release_count++;
                return 0;
            }
            last_key_scan = 0xFF;
            release_count = 0;
            return 0;
        }
    }

    {
        // Always handle these
        if (k == KSCAN_RETURN) return '\r';
        if (k == KSCAN_DEL) return 8;  // Backspace
        if (k == KSCAN_F1) return 'c';  // Open config screen
        if (k == KSCAN_F7) return 0x18;  // Exit (hand off to Ultimate menu)

        // In category menu
        if (current_page == PAGE_CATS)
        {
            if (is_letter_grid())
            {
                // Grid mode: cursor right/left navigate the grid instead of
                // entering a folder / going back. Back still works via DEL.
                if (k == KSCAN_W || (k == KSCAN_CSR_DOWN && shift)) return 'u';
                if (k == KSCAN_S || k == KSCAN_CSR_DOWN) return 'd';
                if (k == KSCAN_CSR_RIGHT && !shift) return 'r';
                if (k == KSCAN_CSR_RIGHT && shift) return 'l';
                if (k == KSCAN_A) return 'l';
                if (k == KSCAN_D) return 'r';
                if (k == KSCAN_SLASH) return '/';
            }
            else
            {
                // F1 (config) / F7 (exit) are handled globally above; no
                // letter shortcuts at this level.
                if (k == KSCAN_W) return 'u';
                if (k == KSCAN_CSR_DOWN && shift) return 'u';
                if (k == KSCAN_S || k == KSCAN_CSR_DOWN) return 'd';
                if (k == KSCAN_SLASH) return '/';
                if (k == KSCAN_CSR_RIGHT && !shift) return '>';  // Right = enter category
                if (k == KSCAN_CSR_RIGHT && shift) return 8;  // Left = back
            }
        }
        // In list view
        else if (current_page == PAGE_LIST)
        {
            if (k == KSCAN_W || (k == KSCAN_CSR_DOWN && shift)) return 'u';
            if (k == KSCAN_S || k == KSCAN_CSR_DOWN) return 'd';
            if (k == KSCAN_N) return 'n';
            if (k == KSCAN_P) return 'p';
            if (k == KSCAN_I) return 'i';  // Info
            if (k == KSCAN_CSR_RIGHT && !shift) return '>';  // Right = releases
            if (k == KSCAN_CSR_RIGHT && shift) return 8;  // Left = back
        }
        // In the config page (server / port / autostart / connect / save).
        else if (current_page == PAGE_SETTINGS)
        {
            if (!settings_editing)
            {
                if (k == KSCAN_W || (k == KSCAN_CSR_DOWN && shift)) return 'u';
                if (k == KSCAN_S || k == KSCAN_CSR_DOWN) return 'd';
            }
            else
            {
                // Editing — digits and '.' for both SERVER (IP/host) and
                // PORT (decimal) fields.
                if (k < 64)
                {
                    byte c = (byte)keyb_codes[shift ? k + 64 : k];
                    if (c >= '0' && c <= '9') return c;
                    if (c == '.') return '.';
                }
            }
        }
        // In simple search. The active region (input box vs result list)
        // determines whether letter scancodes are typing-input or nav
        // shortcuts. Cursor-keys are always nav (no character ambiguity);
        // letters W/S/N/P/I are nav ONLY in the result list, otherwise
        // they're appended to the query.
        else if (current_page == PAGE_SEARCH)
        {
            if (k == KSCAN_CSR_RIGHT && shift) return 8;  // Left = back
            if (k == 0x0F) return '\t';  // C= = cycle category
            if (search_in_box)
            {
                // Cursor-up/down only — leave letters for typing.
                if (k == KSCAN_CSR_DOWN && shift) return 'u';
                if (k == KSCAN_CSR_DOWN) return 'd';
            }
            else if (item_count > 0)
            {
                if (k == KSCAN_W || (k == KSCAN_CSR_DOWN && shift)) return 'u';
                if (k == KSCAN_S || k == KSCAN_CSR_DOWN) return 'd';
                if (k == KSCAN_N) return 'n';
                if (k == KSCAN_P) return 'p';
                if (k == KSCAN_I) return 'i';
                if (k == KSCAN_CSR_RIGHT && !shift) return '\r';
            }
            // Letter / digit / space fallthrough: typed character. Space
            // matters for multi-word queries ("the new"), so it's allowed
            // through here and at the append-to-query default-case below.
            if (k < 64) {
                byte c = (byte)keyb_codes[shift ? k + 64 : k];
                if (c >= 'a' && c <= 'z') return c - 32;
                if ((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) return c;
                if (c == ' ') return ' ';
            }
        }
        // In releases page
        else if (current_page == PAGE_RELEASES)
        {
            if (k == KSCAN_W || (k == KSCAN_CSR_DOWN && shift)) return 'u';
            if (k == KSCAN_S || k == KSCAN_CSR_DOWN) return 'd';
            if (k == KSCAN_N) return 'n';
            if (k == KSCAN_P) return 'p';
            if (k == KSCAN_I) return 'i';  // Info
            if (k == KSCAN_CSR_RIGHT && shift) return 8;  // Left = back
        }
        // In info screen - any key returns
        else if (current_page == PAGE_INFO)
        {
            return 'x';  // Any key = exit info
        }
    }
    return 0;
}

void wait_key(void)
{
    keyb_poll();
    while (keyb_key & KSCAN_QUAL_DOWN)
        keyb_poll();
    while (!(keyb_key & KSCAN_QUAL_DOWN))
    {
        if (DEBUG_KEY_INJECT)
        {
            DEBUG_KEY_INJECT = 0;
            return;
        }
        keyb_poll();
    }
}

//-----------------------------------------------------------------------------
// UI Drawing
//-----------------------------------------------------------------------------

// Draw a single item line (for partial updates)
// row_offset: 4 for normal lists, 2 for adv results
void draw_item_at(int i, bool selected, byte row_offset)
{
    byte y = i + row_offset;
    clear_line(y);
    if (selected)
    {
        print_at_color(0, y, ">", 1);  // White
        print_at_color(2, y, item_names[i], 1);
    }
    else
    {
        print_at_color(2, y, item_names[i], 14);  // Light blue (default)
    }
}

// Draw item for normal list pages (row offset 4) - plain version
void draw_item(int i, bool selected)
{
    draw_item_at(i, selected, 4);
}

// Draw item for grouped list pages (row offset 4) with trainer/release indicators
void draw_grouped_item(int i, bool selected)
{
    byte y = i + 4;  // Row offset 4 for list pages
    clear_line(y);
    byte color = selected ? 1 : 14;

    if (selected)
        print_at_color(0, y, ">", 1);

    print_at_color(2, y, item_names[i], color);

    int name_len = strlen(item_names[i]);
    int x = 2 + name_len + 1;

    // Show trainer count if > 0
    if (item_trainers[i] > 0)
    {
        char trn[6];
        sprintf(trn, "+%d", item_trainers[i]);
        print_at_color(x, y, trn, 13);  // Light green
        x += strlen(trn) + 1;
    }

    // Show ">" indicator for grouped entries (count > 1)
    if (item_counts[i] > 1)
    {
        print_at_color(x, y, ">", 5);  // Green arrow
    }
}

// Update cursor for grouped list pages (preserves trainer/release indicators)
void update_grouped_cursor(int old_cursor, int new_cursor)
{
    if (old_cursor >= 0 && old_cursor < item_count)
        draw_grouped_item(old_cursor, false);
    if (new_cursor >= 0 && new_cursor < item_count)
        draw_grouped_item(new_cursor, true);
}

// Update cursor display without full redraw (only redraws 2 lines)
void update_cursor_at(int old_cursor, int new_cursor, byte row_offset)
{
    if (old_cursor >= 0 && old_cursor < item_count)
        draw_item_at(old_cursor, false, row_offset);
    if (new_cursor >= 0 && new_cursor < item_count)
        draw_item_at(new_cursor, true, row_offset);
}

// Update cursor for normal list pages (row offset 4)
void update_cursor(int old_cursor, int new_cursor)
{
    update_cursor_at(old_cursor, new_cursor, 4);
}

// Letter-grid layout: 3 rows x 9 cols (A..Z + '#' = 27 cells). Each cell is
// 4 chars wide starting at column 2. Cells are repainted individually on
// cursor move via draw_letter_cell to avoid full screen redraws.
#define LETTER_GRID_COLS 9
#define LETTER_GRID_X0   2
#define LETTER_GRID_Y0   3
#define LETTER_GRID_STEP_X 4
#define LETTER_GRID_STEP_Y 2

// True when the current menu should be rendered as a letter grid rather than
// a vertical list. The server marks this by making the menu_path end with
// "/A-Z".
static bool is_letter_grid(void)
{
    int len = strlen(menu_path);
    return len >= 4 && strcmp(menu_path + len - 4, "/A-Z") == 0;
}

static void draw_letter_cell(int idx, bool selected)
{
    byte x = LETTER_GRID_X0 + (idx % LETTER_GRID_COLS) * LETTER_GRID_STEP_X;
    byte y = LETTER_GRID_Y0 + (idx / LETTER_GRID_COLS) * LETTER_GRID_STEP_Y;
    // 1-char name; cell payload is space+letter+space to cleanly erase any
    // previous '>' when the cursor moves through.
    char buf[4];
    buf[0] = selected ? '>' : ' ';
    buf[1] = ' ';
    buf[2] = item_names[idx][0];
    buf[3] = 0;
    // Color 1=white (selected), 14=light blue (active), 11=dark grey (empty
    // letter — the cart shows all 27 cells so the grid layout stays stable;
    // dimming + cursor-skip keeps users from drilling into a 0-result list).
    byte color = selected ? 1 : (item_ids[idx] == 0 ? 11 : 14);
    print_at_color(x, y, buf, color);
}

void update_letter_grid_cursor(int old_cursor, int new_cursor)
{
    if (old_cursor >= 0 && old_cursor < item_count)
        draw_letter_cell(old_cursor, false);
    if (new_cursor >= 0 && new_cursor < item_count)
        draw_letter_cell(new_cursor, true);
}

// Empty letters (count==0) render dim and aren't tappable. Cursor movement
// skips them by scanning in `step` direction (±1 horizontal, ±LETTER_GRID_COLS
// vertical) until a non-empty cell is found or we'd leave the grid.
static int find_next_letter(int from, int step)
{
    int i = from + step;
    while (i >= 0 && i < item_count && item_ids[i] == 0)
        i += step;
    if (i < 0 || i >= item_count) return -1;
    return i;
}

static void draw_letter_grid(const char *title)
{
    clear_screen();
    print_at_color(0, 0, title, 7);  // Yellow
    for (int i = 0; i < item_count; i++)
        draw_letter_cell(i, i == cursor);
    print_at(0, 23, "arrows return:sel /:srch del:back");
}

void draw_list(const char *title)
{
    if (current_page == PAGE_CATS && is_letter_grid())
    {
        draw_letter_grid(title);
        return;
    }

    clear_screen();

    // Title
    print_at_color(0, 0, title, 7);  // Yellow

    // Search input row (PAGE_SEARCH only). Same renderer both paths use so
    // the cursor "_" colour stays consistent on full redraws and partial
    // (typing) updates.
    if (current_page == PAGE_SEARCH)
        draw_search_input();

    // Info line
    if (item_count > 0)
    {
        char info[40];
        sprintf(info, "%d-%d of %ld", offset + 1, offset + item_count, total_count);
        print_at(0, 2, info);
    }
    // Draw items. Selection is suppressed when the cursor is in the search
    // input box: there's no "current row" to highlight in result-list mode.
    bool show_selection = (current_page != PAGE_SEARCH) || !search_in_box;
    for (int i = 0; i < item_count && i < LIST_HEIGHT; i++)
    {
        // Use grouped item drawing for PAGE_LIST (shows trainer/release count)
        if (current_page == PAGE_LIST)
        {
            draw_grouped_item(i, i == cursor);
        }
        else
        {
            byte y = i + 4;
            if (show_selection && i == cursor)
            {
                print_at_color(0, y, ">", 1);  // White
                print_at_color(2, y, item_names[i], 1);
            }
            else
            {
                print_at(2, y, item_names[i]);
            }
        }
    }

    // Help line
    if (current_page == PAGE_CATS)
        print_at(0, 23, "w/s return:sel /:srch f1:cfg f7:exit");
    else if (current_page == PAGE_LIST)
        print_at(0, 23, "w/s return:run i:info del:bk >:rel n/p");
    else if (current_page == PAGE_SEARCH)
    {
        if (search_in_box)
            print_at(0, 23, "type query  return/down:srch  del:back");
        else
            print_at(0, 23, "w/s return:run i:info del:edit");
    }
}

// Repaint the search input row (row 1). Query text and cursor render in
// green so chars typed earlier (during a previous box visit) match the
// colour of chars typed now. The "_" cursor only shows when the cursor is
// in the box; in result-list mode the input row stays static.
static void draw_search_input(void)
{
    clear_line(1);
    print_at(0, 1, "[");
    print_at_color(1, 1, search_cat_names[search_category], 5);
    int x = 1 + strlen(search_cat_names[search_category]);
    print_at(x, 1, "] ");
    x += 2;
    print_at_color(x, 1, search_query, 5);
    if (search_in_box)
        print_at_color(x + search_query_len, 1, "_", 5);
}

// Draw a single config row at row y. Caller has already cleared the screen
// (full redraw) or the row (cell update). value_color picks white for
// "selected, not editing", green for "editing", light blue for "not selected".
static void draw_cfg_field(int field, byte y)
{
    bool selected = (settings_cursor == field);
    bool editing = selected && settings_editing;
    byte label_color = selected ? 1 : 14;
    byte value_color = editing ? 5 : (selected ? 1 : 14);

    if (selected)
        print_at_color(0, y, ">", 1);

    switch (field)
    {
    case CFG_FIELD_SERVER:
        print_at_color(2, y, "server:", label_color);
        if (editing)
        {
            print_at_color(11, y, server_host, value_color);
            print_at_color(11 + settings_edit_pos, y, "_", 5);
        }
        else
        {
            print_at_color(11, y, server_host, value_color);
        }
        break;

    case CFG_FIELD_PORT:
        print_at_color(2, y, "port:", label_color);
        if (editing)
        {
            print_at_color(11, y, port_edit_buf, value_color);
            print_at_color(11 + settings_edit_pos, y, "_", 5);
        }
        else
        {
            char buf[8];
            sprintf(buf, "%d", server_port);
            print_at_color(11, y, buf, value_color);
        }
        break;

    case CFG_FIELD_AUTOCONN:
        print_at_color(2, y, "autostart:", label_color);
        print_at_color(13, y, auto_connect ? "yes" : "no", value_color);
        break;

    case CFG_FIELD_CONNECT:
        print_at_color(2, y, "[connect]", label_color);
        break;

    case CFG_FIELD_SAVE:
        print_at_color(2, y, "[save]", label_color);
        break;
    }
}

// Row offsets used by both full redraw and cursor update.
static byte cfg_row_for_field(int field)
{
    switch (field)
    {
    case CFG_FIELD_SERVER:   return 6;
    case CFG_FIELD_PORT:     return 8;
    case CFG_FIELD_AUTOCONN: return 10;
    case CFG_FIELD_CONNECT:  return 13;
    case CFG_FIELD_SAVE:     return 15;
    }
    return 0;
}

void draw_settings(void)
{
    clear_screen();
    print_at_color(0, 0, "assembly64 (local)", 7);  // Yellow

    // Top diagnostics — captured once at boot, reused on every config redraw
    if (diag_ultimate[0])
        print_at_color(0, 2, diag_ultimate, 3);  // Cyan
    if (diag_ip[0])
        print_at_color(0, 3, diag_ip, 3);

    for (int i = 0; i < CFG_FIELD_COUNT; i++)
    {
        byte y = cfg_row_for_field(i);
        clear_line(y);
        draw_cfg_field(i, y);
    }

    if (settings_editing)
        print_at(0, 23, "type value  return:done  del:erase");
    else
        print_at(0, 23, "w/s return:run/edit del:back f7:exit");
}

// Draw item for releases list with trainer count
void draw_release_item_at(int i, bool selected)
{
    byte y = i + 2;  // Row offset 2 for releases
    clear_line(y);
    byte color = selected ? 1 : 14;

    if (selected)
        print_at_color(0, y, ">", 1);

    print_at_color(2, y, item_names[i], color);

    // Show trainer count if > 0
    if (item_trainers[i] > 0)
    {
        int name_len = strlen(item_names[i]);
        char trn[6];
        sprintf(trn, "+%d", item_trainers[i]);
        print_at_color(2 + name_len + 1, y, trn, 13);  // Light green
    }
}

// Update cursor for releases list (preserves trainer indicator)
void update_releases_cursor(int old_cursor, int new_cursor)
{
    if (old_cursor >= 0 && old_cursor < item_count)
        draw_release_item_at(old_cursor, false);
    if (new_cursor >= 0 && new_cursor < item_count)
        draw_release_item_at(new_cursor, true);
}

void draw_releases(void)
{
    clear_screen();

    // Title: "Title (count)"
    char title[48];
    sprintf(title, "%s (%ld)", releases_title, total_count);
    print_at_color(0, 0, title, 7);  // Yellow

    // Draw items starting at row 2
    int max_display = 19;
    for (int i = 0; i < item_count && i < max_display; i++)
    {
        draw_release_item_at(i, i == cursor);
    }

    // Help line at row 23
    print_at(0, 23, "w/s return:run i:info del:back n/p");
}

void draw_info(void)
{
    clear_screen();
    print_at_color(0, 0, "entry info", 7);  // Yellow

    // Draw all info fields with labels
    for (int i = 0; i < info_line_count; i++)
    {
        byte y = 2 + i;
        // Label in cyan
        print_at_color(2, y, info_labels[i], 3);
        print_at_color(2 + strlen(info_labels[i]), y, ":", 3);
        // Value in white
        print_at_color(10, y, info_values[i], 1);
    }

    print_at(0, 23, "return:run any other key:back");
}

//-----------------------------------------------------------------------------
// Main
//-----------------------------------------------------------------------------

// Set the handful of non-zero defaults that used to live as static
// initializers. Cart targets keep initialized data in read-only ROM, so we
// declare the variables uninitialized (BSS, zero-cleared at startup) and
// assign their actual defaults here at runtime.
static void init_state(void)
{
    strcpy(server_host, DEFAULT_SERVER_HOST);
    server_port = DEFAULT_SERVER_PORT;
    auto_connect = false;
    search_in_box = true;
    releases_return_page = PAGE_LIST;
    last_key_scan = 0xFF;
}

int main(void)
{
    init_state();

    // VIC chip into a known good text-mode state. The KERNAL normally
    // does this on boot; cart targets bypass that path, so the VIC may
    // sit in whatever state the previous program left it (sometimes with
    // the display disabled, or pointing at a non-default screen address).
    // Without this explicit init the C64's video output stays black even
    // though screen RAM at $0400 holds the right characters.
    //   $D011 = 0x1B : display on, text mode, 25 rows, default Y-scroll
    //   $D015 = 0x00 : disable all sprites (KERNAL would zero this; cart
    //                  boot doesn't, and the Ultimate's pre-launch state
    //                  has left sprite 0 enabled with its pointer at
    //                  $07F8 → sprite data at $0800-$083F, which is our
    //                  oscar64 relocated-startup region. VIC renders
    //                  those bytes as a 24x21 "QR-code" block in the
    //                  middle of the screen.)
    //   $D016 = 0xC8 : 40 cols, multicolor off, default X-scroll
    //   $D018 = 0x14 : screen at $0400, charset at $1000
    //   $DD00 = 0x97 : VIC bank 0 ($0000-$3FFF)
    *((volatile byte *)0xDD00) = 0x97;
    *((volatile byte *)0xD018) = 0x14;
    *((volatile byte *)0xD016) = 0xC8;
    *((volatile byte *)0xD015) = 0x00;
    *((volatile byte *)0xD011) = 0x1B;

    // Initialize menu state
    menu_path[0] = 0;

    // Set up screen
    vic.color_border = VCOL_BLACK;
    vic.color_back = VCOL_BLACK;

    // Verify the Ultimate UCI is responding. DOS_CMD_IDENTIFY returns a
    // firmware subsystem string (e.g. "ULTIMATE-II DOS V1.2") — the actual
    // product name (II+ / 64 / C64 Ultimate) is compile-time and not
    // exposed via UCI, so we just show whatever the device tells us.
    //
    // uci_identify() returns false if the UCI command interface is
    // disabled in the Ultimate firmware menu (status reads open-bus
    // 0xFF — would have hung the C64 forever in the original cc65 lib).
    // Loop on failure so the user can fix the firmware setting (or
    // recover from a wedged UCI state caused by a previous run) and
    // retry without re-uploading the .prg.
    while (true)
    {
        clear_screen();
        print_at_color(0, 0, "assembly64 (local)", 7);
        print_at(0, 2, "checking ultimate...");

        if (uci_identify() && uci_success()) break;

        print_at(0, 4, "ultimate not responding!");
        print_at(0, 6, "if command interface is off,");
        print_at(0, 7, "enable it in ultimate menu:");
        print_at(0, 8, "  memory & roms -> command if.");
        print_at(0, 10, "if that has no effect, try the");
        print_at(0, 11, ".crt build instead of the .prg.");
        print_at(0, 13, "return: retry   f7: exit");

        char k;
        do { k = get_key(); } while (k == 0);
        if (k == 0x18) return 1;          // F7 → exit
        // Any other key (Return, etc.) loops back to retry.
    }
    strncpy(diag_ultimate, uci_data, sizeof(diag_ultimate) - 1);
    diag_ultimate[sizeof(diag_ultimate) - 1] = 0;

    uci_getipaddress();
    if (uci_success())
    {
        // 4 raw bytes returned, not ASCII.
        sprintf(diag_ip, "ip: %d.%d.%d.%d",
                (byte)uci_data[0], (byte)uci_data[1],
                (byte)uci_data[2], (byte)uci_data[3]);
    }

    load_settings();

    // Auto-connect path: when the saved flag is set, attempt to connect
    // once silently. Success → straight into the browser, no config UI.
    // Failure → fall through to the config screen with the error in the
    // status line so the user can fix something and retry.
    bool autostarted = false;
    if (auto_connect)
        autostarted = connect_to_server();

    if (autostarted)
    {
        load_categories();
        current_page = PAGE_CATS;
        draw_list("assembly64 - categories");
    }
    else
    {
        current_page = PAGE_SETTINGS;
        settings_cursor = CFG_FIELD_CONNECT;  // ENTER retries connect
        settings_editing = false;
        draw_settings();
    }

    bool running = true;

    while (running)
    {
        char key = get_key();

        if (key != 0)
        {
            // Get current title
            const char *title = "assembly64 - categories";
            if (current_page == PAGE_LIST)
                title = current_category;

            switch (key)
            {
            case 'c':  // F1 from any page → config screen
                if (current_page != PAGE_SETTINGS && current_page != PAGE_INFO)
                {
                    current_page = PAGE_SETTINGS;
                    settings_cursor = CFG_FIELD_SERVER;
                    settings_editing = false;
                    draw_settings();
                }
                break;

            case '/':  // Open search: cursor starts in the input box
                if (current_page == PAGE_CATS)
                {
                    current_page = PAGE_SEARCH;
                    search_in_box = true;
                    search_query[0] = 0;
                    search_query_len = 0;
                    search_category = 0;
                    item_count = 0;
                    total_count = 0;
                    cursor = 0;
                    offset = 0;
                    draw_list("assembly64 - search");
                }
                break;

            case '\t':  // Cycle category filter while in search box only
                if (current_page == PAGE_SEARCH && search_in_box)
                {
                    search_category = (search_category + 1) & 3;
                    draw_search_input();
                }
                break;

            case 'u':  // Up (W key or cursor up)
                if (current_page == PAGE_SETTINGS)
                {
                    if (settings_cursor > 0)
                    {
                        settings_cursor--;
                        draw_settings();
                    }
                }
                else if (current_page == PAGE_SEARCH && !search_in_box)
                {
                    if (cursor > 0)
                    {
                        int old = cursor;
                        cursor--;
                        update_cursor(old, cursor);
                    }
                    else if (offset > 0)
                    {
                        int new_offset = offset - 20;
                        if (new_offset < 0) new_offset = 0;
                        do_search(search_query, new_offset);
                        cursor = item_count - 1;
                        if (cursor > LIST_HEIGHT - 1) cursor = LIST_HEIGHT - 1;
                        draw_list("assembly64 - search");
                    }
                    else
                    {
                        // At the top of the result list — return to the input box.
                        search_in_box = true;
                        draw_list("assembly64 - search");
                    }
                }
                else if (current_page == PAGE_RELEASES)
                {
                    if (cursor > 0)
                    {
                        int old = cursor;
                        cursor--;
                        update_releases_cursor(old, cursor);
                    }
                    else if (offset > 0)
                    {
                        int new_offset = offset - 20;
                        if (new_offset < 0) new_offset = 0;
                        do_releases(releases_category, releases_title, new_offset);
                        cursor = item_count - 1;
                        if (cursor > 18) cursor = 18;
                        draw_releases();
                    }
                }
                else if (current_page == PAGE_LIST)
                {
                    if (cursor > 0)
                    {
                        int old = cursor;
                        cursor--;
                        update_grouped_cursor(old, cursor);
                    }
                    else if (offset > 0)
                    {
                        // At top of list, go to previous page
                        int new_offset = offset - 20;
                        if (new_offset < 0) new_offset = 0;
                        load_list_path(current_category, new_offset);
                        cursor = item_count - 1;
                        if (cursor > LIST_HEIGHT - 1) cursor = LIST_HEIGHT - 1;
                        draw_list(current_category);
                    }
                }
                else if (current_page == PAGE_CATS && is_letter_grid())
                {
                    // Letter grid: step up one row (= -LETTER_GRID_COLS),
                    // skipping empty (zero-count) letters.
                    int target = find_next_letter(cursor, -LETTER_GRID_COLS);
                    if (target >= 0)
                    {
                        int old = cursor;
                        cursor = target;
                        update_letter_grid_cursor(old, cursor);
                    }
                }
                else if (cursor > 0)
                {
                    int old = cursor;
                    cursor--;
                    update_cursor(old, cursor);
                }
                break;

            case 'd':  // Down (S key or cursor down)
                if (current_page == PAGE_SETTINGS)
                {
                    if (settings_cursor < CFG_FIELD_COUNT - 1)
                    {
                        settings_cursor++;
                        draw_settings();
                    }
                }
                else if (current_page == PAGE_SEARCH && search_in_box)
                {
                    // Down from box: run the search if we have enough chars
                    // and move into the result list.
                    if (search_query_len >= SEARCH_MIN_QUERY_LEN)
                    {
                        do_search(search_query, 0);
                        search_in_box = false;
                        cursor = 0;
                        draw_list("assembly64 - search");
                    }
                }
                else if (current_page == PAGE_SEARCH)
                {
                    int max_visible = LIST_HEIGHT - 1;
                    if (cursor < item_count - 1 && cursor < max_visible)
                    {
                        int old = cursor;
                        cursor++;
                        update_cursor(old, cursor);
                    }
                    else if (offset + item_count < total_count)
                    {
                        do_search(search_query, offset + 20);
                        cursor = 0;
                        draw_list("assembly64 - search");
                    }
                }
                else if (current_page == PAGE_RELEASES)
                {
                    int max_visible = 18;
                    if (cursor < item_count - 1 && cursor < max_visible)
                    {
                        int old = cursor;
                        cursor++;
                        update_releases_cursor(old, cursor);
                    }
                    else if (offset + item_count < total_count)
                    {
                        do_releases(releases_category, releases_title, offset + 20);
                        cursor = 0;
                        draw_releases();
                    }
                }
                else if (current_page == PAGE_LIST)
                {
                    // Max visible is LIST_HEIGHT - 1 (0 to 17 for 18 items)
                    int max_visible = LIST_HEIGHT - 1;
                    if (cursor < item_count - 1 && cursor < max_visible)
                    {
                        int old = cursor;
                        cursor++;
                        update_grouped_cursor(old, cursor);
                    }
                    else if (offset + item_count < total_count)
                    {
                        // At bottom, go to next page
                        load_list_path(current_category, offset + 20);
                        cursor = 0;
                        draw_list(current_category);
                    }
                }
                else if (current_page == PAGE_CATS && is_letter_grid())
                {
                    // Letter grid: step down one row (= +LETTER_GRID_COLS),
                    // skipping empty (zero-count) letters.
                    int target = find_next_letter(cursor, LETTER_GRID_COLS);
                    if (target >= 0)
                    {
                        int old = cursor;
                        cursor = target;
                        update_letter_grid_cursor(old, cursor);
                    }
                }
                else if (cursor < item_count - 1)
                {
                    int old = cursor;
                    cursor++;
                    update_cursor(old, cursor);
                }
                break;

            case 'l':  // Grid left (letter grid only)
                if (current_page == PAGE_CATS && is_letter_grid())
                {
                    // Skip empty cells in the same row; "wall bump" once we
                    // run off column 0.
                    int target = find_next_letter(cursor, -1);
                    if (target < 0 || target / LETTER_GRID_COLS != cursor / LETTER_GRID_COLS)
                    {
                        go_back();
                    }
                    else
                    {
                        int old = cursor;
                        cursor = target;
                        update_letter_grid_cursor(old, cursor);
                    }
                }
                break;

            case 'r':  // Grid right (letter grid only)
                if (current_page == PAGE_CATS && is_letter_grid())
                {
                    int target = find_next_letter(cursor, 1);
                    if (target >= 0 && target / LETTER_GRID_COLS == cursor / LETTER_GRID_COLS)
                    {
                        int old = cursor;
                        cursor = target;
                        update_letter_grid_cursor(old, cursor);
                    }
                }
                break;

            case '>':  // Right arrow - enter menu item or releases
                if (current_page == PAGE_CATS && item_count > 0)
                {
                    if (menu_types[cursor] == 'f' || menu_types[cursor] == 'D')
                    {
                        // Folder or Directory - navigate into it
                        load_menu(menu_paths[cursor]);
                        draw_list(item_names[0] ? menu_path : "assembly64");
                    }
                    else if (menu_types[cursor] == 'F')
                    {
                        // File in MYFILES - run it, or upload-only for firmware (.ue2).
                        open_myfile(menu_paths[cursor]);
                    }
                    else
                    {
                        // List or Browse (type 'l' or 'b') - load entries
                        menu_return_cursor = cursor;
                        strcpy(current_category, menu_paths[cursor]);
                        load_list_path(current_category, 0);
                        current_page = PAGE_LIST;
                        draw_list(current_category);
                    }
                }
                else if (current_page == PAGE_LIST && item_count > 0)
                {
                    // Enter releases sub-list if count > 1
                    if (item_counts[cursor] > 1)
                    {
                        strncpy(releases_title, item_names[cursor], 31);
                        releases_title[31] = 0;
                        strncpy(releases_category, current_category, 47);
                        releases_category[47] = 0;
                        releases_return_page = PAGE_LIST;
                        releases_return_offset = offset;
                        releases_return_cursor = cursor;
                        do_releases(releases_category, releases_title, 0);
                        current_page = PAGE_RELEASES;
                        draw_releases();
                    }
                    else
                    {
                        // Single release - just run it
                        run_entry(item_ids[cursor]);
                    }
                }
                break;

            case '\r':  // Enter
                if (current_page == PAGE_CATS && item_count > 0)
                {
                    // Select menu item
                    if (menu_types[cursor] == 'f' || menu_types[cursor] == 'D')
                    {
                        // Folder or Directory - navigate into it
                        load_menu(menu_paths[cursor]);
                        draw_list(menu_path[0] ? menu_path : "assembly64 - categories");
                    }
                    else if (menu_types[cursor] == 'F')
                    {
                        // File in MYFILES - run it, or upload-only for firmware (.ue2).
                        open_myfile(menu_paths[cursor]);
                    }
                    else
                    {
                        // List or Browse (type 'l' or 'b') - load entries
                        menu_return_cursor = cursor;
                        strcpy(current_category, menu_paths[cursor]);
                        load_list_path(current_category, 0);
                        current_page = PAGE_LIST;
                        draw_list(current_category);
                    }
                }
                else if (current_page == PAGE_SETTINGS)
                {
                    if (settings_editing)
                    {
                        // Confirm edit: parse PORT back into server_port if
                        // we were editing that field; SERVER buffer is
                        // already live-edited in place.
                        if (settings_cursor == CFG_FIELD_PORT)
                        {
                            int p = atoi(port_edit_buf);
                            if (p > 0 && p < 65536)
                                server_port = p;
                        }
                        settings_editing = false;
                        draw_settings();
                    }
                    else
                    {
                        switch (settings_cursor)
                        {
                        case CFG_FIELD_SERVER:
                            settings_editing = true;
                            settings_edit_pos = strlen(server_host);
                            draw_settings();
                            break;
                        case CFG_FIELD_PORT:
                            settings_editing = true;
                            sprintf(port_edit_buf, "%d", server_port);
                            settings_edit_pos = strlen(port_edit_buf);
                            draw_settings();
                            break;
                        case CFG_FIELD_AUTOCONN:
                            auto_connect = !auto_connect;
                            draw_settings();
                            break;
                        case CFG_FIELD_CONNECT:
                            // Try connecting now. On success enter the
                            // browser; on failure stay on this page so the
                            // user can fix something and try again.
                            if (connect_to_server())
                            {
                                load_categories();
                                current_page = PAGE_CATS;
                                draw_list("assembly64 - categories");
                            }
                            // print_status already shows the error from
                            // connect_to_server when it failed.
                            break;
                        case CFG_FIELD_SAVE:
                            print_status("saving...");
                            save_settings();
                            print_status("saved!");
                            break;
                        }
                    }
                }
                else if (current_page == PAGE_SEARCH && search_in_box)
                {
                    // ENTER in the input box: run the query and move cursor
                    // into the result list.
                    if (search_query_len >= SEARCH_MIN_QUERY_LEN)
                    {
                        do_search(search_query, 0);
                        search_in_box = false;
                        cursor = 0;
                        draw_list("assembly64 - search");
                    }
                }
                else if (current_page == PAGE_RELEASES && item_count > 0)
                {
                    // Run selected release
                    run_entry(item_ids[cursor]);
                }
                else if (item_count > 0)
                {
                    // Run entry (PAGE_LIST or PAGE_SEARCH-result-list)
                    run_entry(item_ids[cursor]);
                }
                break;

            case 0x18:  // F7 - exit to BASIC (EF) / Ultimate menu (PRG).
                disconnect_from_server();
#ifdef OSCAR_TARGET_CRT_EASYFLASH
                // Disable the cart and jump straight to KERNAL_RESET.
                // The full KERNAL cold-start path runs (IOINIT, CIA
                // setup, RAMTAS, RESTOR, CINT, then JMP ($A000) →
                // BASIC cold-start) and lands on the standard banner
                // + READY prompt — the same end state as a hardware
                // reset. Earlier attempts to do partial resets (CINT
                // + BASIC NEW) printed "READY." but left the system
                // in a fragile state; subsequent F7 keypresses or
                // KERNAL/JiffyDOS interactions could hang. Going
                // through the full $FCE2 path makes the system
                // indistinguishable from a fresh boot.
                //
                //  $DE02 ← $04   Disable the EasyFlash. On the C64
                //                Ultimate firmware (1.1.0) this is
                //                the only $DE02 value that actually
                //                hides the cart at $8000-$BFFF and
                //                $E000-$FFFF — the spec-standard
                //                $87 leaves the cart mapped at
                //                $8000-$BFFF on this firmware.
                //                Verified by walking $DE02 across
                //                $00..$87 and reading the cart
                //                signature at $8000.
                //
                //                IMPORTANT: only works when the
                //                firmware's ACIA modem mapping is
                //                "Off". With the default DE00/NMI
                //                SwiftLink, $DE02 writes are
                //                intercepted by the ACIA command
                //                register and never reach EF.
                //  JMP $FCE2    KERNAL cold-reset entry. Everything
                //                else (CIA init, $0001, screen,
                //                vectors, banner) is handled by
                //                KERNAL exactly as on a real reset
                //                because the cart is truly hidden
                //                from the bus by the time KERNAL
                //                starts reading from $E000.
                __asm {
                    lda #0x04
                    sta 0xDE02
                    jmp 0xFCE2
                }
#else
                uci_pop_ultimate_menu();
#endif
                break;

            case 8:  // Backspace
                if (current_page == PAGE_SETTINGS)
                {
                    if (settings_editing && settings_edit_pos > 0)
                    {
                        settings_edit_pos--;
                        if (settings_cursor == CFG_FIELD_SERVER)
                            server_host[settings_edit_pos] = 0;
                        else if (settings_cursor == CFG_FIELD_PORT)
                            port_edit_buf[settings_edit_pos] = 0;
                        draw_settings();
                    }
                    else if (!settings_editing && connected)
                    {
                        // Came from main menu via F1: go back to categories.
                        current_page = PAGE_CATS;
                        draw_list("assembly64 - categories");
                    }
                }
                else if (current_page == PAGE_LIST || current_page == PAGE_CATS)
                {
                    // Go back
                    go_back();
                }
                else if (current_page == PAGE_SEARCH && !search_in_box)
                {
                    // From results: return to the input box, preserving the
                    // query so the user can refine it.
                    search_in_box = true;
                    draw_list("assembly64 - search");
                }
                else if (current_page == PAGE_SEARCH)
                {
                    // In the input box: backspace, or exit when empty.
                    if (search_query_len > 0)
                    {
                        search_query_len--;
                        search_query[search_query_len] = 0;
                        draw_search_input();
                    }
                    else
                    {
                        load_categories();
                        draw_list("assembly64 - categories");
                    }
                }
                else if (current_page == PAGE_RELEASES)
                {
                    // Go back
                    go_back();
                }
                else if (current_page == PAGE_INFO)
                {
                    // DEL on info screen — return to entry list, same as
                    // any other non-ENTER key. Goes through the same
                    // path 'x' uses below.
                    current_page = info_return_page;
                    if (current_page == PAGE_LIST)
                        draw_list(current_category);
                    else if (current_page == PAGE_SEARCH)
                        draw_list("assembly64 - search");
                    else if (current_page == PAGE_RELEASES)
                        draw_releases();
                }
                break;

            case 'n':  // Next page
                if (current_page == PAGE_LIST && offset + item_count < total_count)
                {
                    load_list_path(current_category, offset + 20);
                    draw_list(current_category);
                }
                else if (current_page == PAGE_SEARCH && offset + item_count < total_count)
                {
                    do_search(search_query, offset + 20);
                    draw_list("assembly64 - search");
                }
                else if (current_page == PAGE_RELEASES && offset + item_count < total_count)
                {
                    do_releases(releases_category, releases_title, offset + 20);
                    draw_releases();
                }
                break;

            case 'p':  // Previous page
                if (current_page == PAGE_LIST && offset > 0)
                {
                    int new_offset = offset - 20;
                    if (new_offset < 0) new_offset = 0;
                    load_list_path(current_category, new_offset);
                    draw_list(current_category);
                }
                else if (current_page == PAGE_SEARCH && offset > 0)
                {
                    int new_offset = offset - 20;
                    if (new_offset < 0) new_offset = 0;
                    do_search(search_query, new_offset);
                    draw_list("assembly64 - search");
                }
                else if (current_page == PAGE_RELEASES && offset > 0)
                {
                    int new_offset = offset - 20;
                    if (new_offset < 0) new_offset = 0;
                    do_releases(releases_category, releases_title, new_offset);
                    draw_releases();
                }
                break;

            case 'i':  // Info
                if ((current_page == PAGE_LIST || current_page == PAGE_SEARCH ||
                     current_page == PAGE_RELEASES) && item_count > 0)
                {
                    // Remember where to return
                    info_return_page = current_page;
                    // Fetch and display info
                    if (fetch_info(item_ids[cursor]))
                    {
                        current_page = PAGE_INFO;
                        draw_info();
                    }
                }
                break;

            case 'x':  // Exit info screen
                if (current_page == PAGE_INFO)
                {
                    current_page = info_return_page;
                    if (current_page == PAGE_LIST)
                        draw_list(current_category);
                    else if (current_page == PAGE_SEARCH)
                        draw_list("assembly64 - search");
                    else if (current_page == PAGE_RELEASES)
                        draw_releases();
                }
                break;

            default:
                // Typed alphanumeric: append to query, but only when the
                // cursor is in the search input box. In result-list mode
                // the same letter scancodes are ignored; the user must DEL
                // back to the box first.
                if (current_page == PAGE_SEARCH && search_in_box &&
                    ((key >= 'A' && key <= 'Z') || (key >= '0' && key <= '9') || key == ' '))
                {
                    if (search_query_len < (int)sizeof(search_query) - 1)
                    {
                        search_query[search_query_len++] = key;
                        search_query[search_query_len] = 0;
                        draw_search_input();
                    }
                }
                // Typed character in settings edit mode. SERVER takes IP-
                // style chars, PORT takes digits only — '.' silently dropped
                // there.
                else if (current_page == PAGE_SETTINGS && settings_editing &&
                         ((key >= '0' && key <= '9') || key == '.'))
                {
                    if (settings_cursor == CFG_FIELD_SERVER && settings_edit_pos < 30)
                    {
                        server_host[settings_edit_pos++] = key;
                        server_host[settings_edit_pos] = 0;
                        draw_settings();
                    }
                    else if (settings_cursor == CFG_FIELD_PORT && key != '.' &&
                             settings_edit_pos < 5)
                    {
                        port_edit_buf[settings_edit_pos++] = key;
                        port_edit_buf[settings_edit_pos] = 0;
                        draw_settings();
                    }
                }
                break;
            }
        }
    }

    disconnect_from_server();
    clear_screen();
    print_at(0, 0, "goodbye!");

    return 0;
}
