/*****************************************************************
 * Ultimate 64/II+ Command Library for Oscar64
 *
 * Ported from cc65 version by Scott Hutter, Francesco Sblendorio
 * (https://github.com/xlar54/ultimateii-dos-lib)
 * Oscar64 port for c64uploader project
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Derived from xlar54/ultimateii-dos-lib (GPL v3) and therefore
 * distributed under the same terms. See ../LICENSE for the full text.
 *****************************************************************/

#include <string.h>
#include <stdlib.h>
#include "ultimate.h"

// Global buffers
char uci_status[UCI_STATUS_QUEUE_SZ];
char uci_data[UCI_DATA_QUEUE_SZ * 2];

// Runtime-resolved I/O window. Set by uci_identify() — see the header
// for why both $DE1C and $DF1C are considered. No initializers because
// cart targets place initialized statics in read-only ROM; these end
// up in BSS (NULL at boot) and must be set before any UCI op fires.
volatile uint8_t *uci_control_reg;
volatile uint8_t *uci_status_reg;
volatile uint8_t *uci_cmd_data_reg;
volatile uint8_t *uci_id_reg;
volatile uint8_t *uci_resp_data_reg;
volatile uint8_t *uci_status_data_reg;

static void uci_set_window(uint16_t base)
{
    uci_control_reg     = (volatile uint8_t*)(base + 0);
    uci_status_reg      = (volatile uint8_t*)(base + 0);
    uci_cmd_data_reg    = (volatile uint8_t*)(base + 1);
    uci_id_reg          = (volatile uint8_t*)(base + 1);
    uci_resp_data_reg   = (volatile uint8_t*)(base + 2);
    uci_status_data_reg = (volatile uint8_t*)(base + 3);
}

// Probe a candidate I/O window for a live UCI. A live window has at
// least one of the four bytes returning something other than open-bus
// 0xFF; a dead window reads all 0xFF in I/O space when the firmware
// hasn't mapped anything there.
static bool uci_window_alive(uint16_t base)
{
    uint8_t i;
    for (i = 0; i < 4; i++)
    {
        if (*((volatile uint8_t*)(base + i)) != 0xFF) return true;
    }
    return false;
}

// Internal state. No initializers — cart targets place initialized statics
// in read-only ROM. uci_target is set to UCI_TARGET_DOS1 lazily by callers
// (every uci_settarget(...) does so explicitly), so leaving it 0 here is
// safe — the first real operation will override it.
static uint8_t uci_target;
static int uci_data_index;
static int uci_data_len;
static char temp_char[2];

// Set to true once any poll loop sees the open-bus 0xFF pattern (UCI
// disabled in firmware) or exhausts its iteration budget. Once tripped,
// every higher-level operation short-circuits at entry instead of
// spinning on dead registers. Callers detect it via uci_identify()
// returning false at boot.
static bool uci_failed;

//-----------------------------------------------------------------------------
// Bounded poll helpers
//
// The original cc65 library used unbounded `while (status & mask)` loops.
// That deadlocks the C64 if UCI is disabled in firmware (registers read
// open-bus 0xFF — see ultimate.h header comment) or if the device gets
// into a weird state. These helpers bail on either condition.
//
// UCI_POLL_LIMIT iterations of a status read is roughly 1–2 seconds on a
// 1 MHz 6502 — orders of magnitude longer than any legitimate UCI op.
//-----------------------------------------------------------------------------

#define UCI_POLL_LIMIT 65535U

// uci_failed is only set on the permanent "UCI disabled" signal — a
// run of UCI_OPENBUS_THRESHOLD consecutive 0xFF reads with no real
// data in between. Single transient 0xFF reads (which we observed on
// real hardware right after C64 reset on an enabled UCI) are tolerated.
// A plain poll-limit timeout returns false but does NOT set the global
// flag, so a retry of the same operation (e.g. the user fixing a wrong
// server address and re-connecting) can still run.

#define UCI_OPENBUS_THRESHOLD 1024  // consecutive 0xFF reads → "UCI dead"

static bool uci_wait_until(uint8_t mask, uint8_t expected)
{
    uint16_t poll = UCI_POLL_LIMIT;
    uint16_t openbus = 0;
    uint8_t s;
    while (poll--)
    {
        s = *UCI_STATUS_REG;
        if (s == 0xFF)
        {
            if (++openbus >= UCI_OPENBUS_THRESHOLD)
            {
                uci_failed = true;
                return false;
            }
            continue;  // never let 0xFF accidentally satisfy the predicate
        }
        openbus = 0;
        if ((s & mask) == expected) return true;
    }
    return false;
}

static bool uci_wait_while(uint8_t mask, uint8_t state)
{
    uint16_t poll = UCI_POLL_LIMIT;
    uint16_t openbus = 0;
    uint8_t s;
    while (poll--)
    {
        s = *UCI_STATUS_REG;
        if (s == 0xFF)
        {
            if (++openbus >= UCI_OPENBUS_THRESHOLD)
            {
                uci_failed = true;
                return false;
            }
            continue;
        }
        openbus = 0;
        if ((s & mask) != state) return true;
    }
    return false;
}

//-----------------------------------------------------------------------------
// Low-level register access
//-----------------------------------------------------------------------------

void uci_settarget(uint8_t id)
{
    uci_target = id;
}

// A single 0xFF read here just means "no data" — do not set uci_failed.
// uci_failed is reserved for the sustained-0xFF case detected inside
// the wait helpers (1024 consecutive open-bus reads). Tripping it on
// one transient 0xFF caused every subsequent op to short-circuit, which
// manifested as "the second connect attempt silently does nothing"
// after the first one failed.
bool uci_isdataavailable(void)
{
    uint8_t s;
    if (uci_failed) return false;
    s = *UCI_STATUS_REG;
    if (s == 0xFF) return false;
    return (s & UCI_STAT_DATA_AV) != 0;
}

bool uci_isstatusdataavailable(void)
{
    uint8_t s;
    if (uci_failed) return false;
    s = *UCI_STATUS_REG;
    if (s == 0xFF) return false;
    return (s & UCI_STAT_STAT_AV) != 0;
}

void uci_sendcommand(uint8_t *bytes, int count)
{
    int x;
    int success = 0;

    if (uci_failed) return;

    bytes[0] = uci_target;

    while (success == 0)
    {
        // Wait for idle state: bits 5 and 4 both clear ((s & 0x30) == 0)
        if (!uci_wait_until(0x30, 0x00)) return;

        // Write bytes to command data register
        for (x = 0; x < count; x++)
            *UCI_CMD_DATA_REG = bytes[x];

        // Push command
        *UCI_CONTROL_REG = UCI_CTRL_PUSH_CMD;

        // Check for error
        if (*UCI_STATUS_REG & UCI_STAT_ERROR)
        {
            // Clear error and try again
            *UCI_CONTROL_REG = UCI_CTRL_CLR_ERR;
        }
        else
        {
            // Wait for command to complete (leave the busy state 0x10)
            if (!uci_wait_while(0x30, 0x10)) return;
            success = 1;
        }
    }
}

void uci_accept(void)
{
    if (uci_failed) return;
    // Acknowledge the data, then wait for the device to clear DATA_ACC
    *UCI_CONTROL_REG |= UCI_CTRL_DATA_ACC;
    uci_wait_while(UCI_STAT_DATA_ACC, UCI_STAT_DATA_ACC);
}

void uci_abort(void)
{
    *UCI_CONTROL_REG |= UCI_CTRL_ABORT;
}

int uci_readdata(void)
{
    int count = 0;
    int max = UCI_DATA_QUEUE_SZ * 2 - 1;
    uci_data[0] = 0;

    while (uci_isdataavailable() && count < max)
    {
        uci_data[count++] = *UCI_RESP_DATA_REG;
    }
    uci_data[count] = 0;
    return count;
}

int uci_readstatus(void)
{
    int count = 0;
    int max = UCI_STATUS_QUEUE_SZ - 1;
    uci_status[0] = 0;

    // For slower ops (TCP connect, file open) the firmware can set
    // DATA_AV before STAT_AV. Without this short poll, uci_readstatus
    // sometimes returns empty even on success, and uci_success() reports
    // a phantom failure. 1024 iters is plenty for normal firmware delays
    // and keeps the worst-case overhead per call low — important because
    // some hot paths (e.g. uci_tcp_nextchar's 5000-retry loop) call
    // through readstatus thousands of times.
    if (!uci_failed)
    {
        uint16_t poll = 1024;
        while (poll-- && !uci_isstatusdataavailable()) {}
    }

    while (uci_isstatusdataavailable() && count < max)
    {
        uci_status[count++] = *UCI_STATUS_DATA_REG;
    }
    uci_status[count] = 0;
    return count;
}

//-----------------------------------------------------------------------------
// Identification
//-----------------------------------------------------------------------------

// Drain any stale UCI state left over from a previous PRG run or a
// partially-completed operation. Bounded by `max_iters` so we never
// hang regardless of how the firmware is misbehaving.
static void uci_drain_stale_state(uint16_t max_iters)
{
    uint8_t s;
    *UCI_CONTROL_REG = UCI_CTRL_ABORT;
    while (max_iters--)
    {
        s = *UCI_STATUS_REG;
        if (s == 0xFF) return;                          // open bus
        if ((s & 0x30) == 0 && (s & 0xC0) == 0) return; // idle, nothing pending
        if (s & UCI_STAT_ERROR)         { *UCI_CONTROL_REG = UCI_CTRL_CLR_ERR;  continue; }
        if (s & UCI_STAT_DATA_AV)       { (void)*UCI_RESP_DATA_REG;             continue; }
        if (s & UCI_STAT_STAT_AV)       { (void)*UCI_STATUS_DATA_REG;           continue; }
        if (s & UCI_STAT_DATA_ACC)      { *UCI_CONTROL_REG = UCI_CTRL_DATA_ACC; continue; }
        // State bits 5/4 set with nothing else to drain — firmware is
        // mid-command. Give it a few more polls to settle.
    }
}

bool uci_identify(void)
{
    uint8_t cmd[] = {0x00, DOS_CMD_IDENTIFY};

    // Clear stale failure state so the user can retry after fixing
    // firmware settings. The bounded poll helpers will set uci_failed
    // back to true if the status register stays at open-bus 0xFF for
    // long enough during the operation below (UCI disabled in firmware).
    uci_failed = false;

    // Probe both possible I/O windows and pick whichever one is mapped
    // by the firmware. The compile-time target only flips probe order:
    //   CRT (EasyFlash claims $DF00) → try $DE1C first
    //   PRG                          → try $DF1C first
    // If neither answers, point at the preferred default and let the
    // bounded poll helpers report the failure with a clear error.
#ifdef OSCAR_TARGET_CRT_EASYFLASH
    if      (uci_window_alive(0xDE1C)) uci_set_window(0xDE1C);
    else if (uci_window_alive(0xDF1C)) uci_set_window(0xDF1C);
    else                                uci_set_window(0xDE1C);
#else
    if      (uci_window_alive(0xDF1C)) uci_set_window(0xDF1C);
    else if (uci_window_alive(0xDE1C)) uci_set_window(0xDE1C);
    else                                uci_set_window(0xDF1C);
#endif

    // Recover from any wedged state before issuing the real command.
    // Without this, a soft-reset / re-load that lands while firmware
    // still has unread data from a prior session causes the wait_until
    // in uci_sendcommand below to time out and falsely report "UCI
    // disabled" even though UCI is fine.
    uci_drain_stale_state(256);

    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();

    return !uci_failed;
}

//-----------------------------------------------------------------------------
// Directory operations
//-----------------------------------------------------------------------------

void uci_get_path(void)
{
    uint8_t cmd[] = {0x00, DOS_CMD_GET_PATH};
    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();
}

void uci_open_dir(void)
{
    uint8_t cmd[] = {0x00, DOS_CMD_OPEN_DIR};
    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, 2);
    uci_readstatus();
    uci_accept();
}

void uci_get_dir(void)
{
    uint8_t cmd[] = {0x00, DOS_CMD_READ_DIR};
    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, 2);
}

void uci_change_dir(const char *directory)
{
    int len = strlen(directory);
    uint8_t *cmd = (uint8_t *)malloc(len + 2);
    int x;
    if (!cmd) return;

    cmd[0] = 0x00;
    cmd[1] = DOS_CMD_CHANGE_DIR;
    for (x = 0; x < len; x++)
        cmd[x + 2] = directory[x];

    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, len + 2);
    free(cmd);

    uci_readstatus();
    uci_accept();
}

void uci_create_dir(const char *directory)
{
    int len = strlen(directory);
    uint8_t *cmd = (uint8_t *)malloc(len + 2);
    int x;
    if (!cmd) return;

    cmd[0] = 0x00;
    cmd[1] = DOS_CMD_CREATE_DIR;
    for (x = 0; x < len; x++)
        cmd[x + 2] = directory[x];

    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, len + 2);
    free(cmd);

    uci_readdata();
    uci_readstatus();
    uci_accept();
}

void uci_change_dir_home(void)
{
    uint8_t cmd[] = {0x00, DOS_CMD_COPY_HOME_PATH};
    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, 2);
    uci_readstatus();
    uci_accept();
}

//-----------------------------------------------------------------------------
// File operations
//-----------------------------------------------------------------------------

void uci_open_file(uint8_t attrib, const char *filename)
{
    int len = strlen(filename);
    uint8_t *cmd = (uint8_t *)malloc(len + 3);
    int x;
    if (!cmd) return;

    cmd[0] = 0x00;
    cmd[1] = DOS_CMD_OPEN_FILE;
    cmd[2] = attrib;
    for (x = 0; x < len; x++)
        cmd[x + 3] = filename[x];

    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, len + 3);
    free(cmd);

    uci_readdata();
    uci_readstatus();
    uci_accept();
}

void uci_close_file(void)
{
    uint8_t cmd[] = {0x00, DOS_CMD_CLOSE_FILE};
    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();
}

void uci_read_file(uint8_t length)
{
    uint8_t cmd[] = {0x00, DOS_CMD_READ_DATA, 0x00, 0x00};
    cmd[2] = length & 0xFF;
    cmd[3] = length >> 8;

    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, 4);
}

void uci_write_file(uint8_t *data, int length)
{
    uint8_t *cmd = (uint8_t *)malloc(length + 4);
    int x;
    if (!cmd) return;

    cmd[0] = 0x00;
    cmd[1] = DOS_CMD_WRITE_DATA;
    cmd[2] = length & 0xFF;         // Length low byte
    cmd[3] = (length >> 8) & 0xFF;  // Length high byte
    for (x = 0; x < length; x++)
        cmd[x + 4] = data[x];

    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, length + 4);
    free(cmd);

    uci_readdata();
    uci_readstatus();
    uci_accept();
}

void uci_delete_file(const char *filename)
{
    int len = strlen(filename);
    uint8_t *cmd = (uint8_t *)malloc(len + 2);
    int x;
    if (!cmd) return;

    cmd[0] = 0x00;
    cmd[1] = DOS_CMD_DELETE_FILE;
    for (x = 0; x < len; x++)
        cmd[x + 2] = filename[x];

    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, len + 2);
    free(cmd);

    uci_readstatus();
    uci_accept();
}

void uci_rename_file(const char *filename, const char *newname)
{
    int len1 = strlen(filename);
    int len2 = strlen(newname);
    uint8_t *cmd = (uint8_t *)malloc(len1 + len2 + 3);
    int x;
    if (!cmd) return;

    cmd[0] = 0x00;
    cmd[1] = DOS_CMD_RENAME_FILE;
    for (x = 0; x < len1; x++)
        cmd[x + 2] = filename[x];
    cmd[len1 + 2] = 0x00;
    for (x = 0; x < len2; x++)
        cmd[len1 + 3 + x] = newname[x];

    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, len1 + len2 + 3);
    free(cmd);

    uci_readstatus();
    uci_accept();
}

void uci_copy_file(const char *sourcefile, const char *destfile)
{
    int len1 = strlen(sourcefile);
    int len2 = strlen(destfile);
    uint8_t *cmd = (uint8_t *)malloc(len1 + len2 + 3);
    int x;
    if (!cmd) return;

    cmd[0] = 0x00;
    cmd[1] = DOS_CMD_COPY_FILE;
    for (x = 0; x < len1; x++)
        cmd[x + 2] = sourcefile[x];
    cmd[len1 + 2] = 0x00;
    for (x = 0; x < len2; x++)
        cmd[len1 + 3 + x] = destfile[x];

    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, len1 + len2 + 3);
    free(cmd);

    uci_readstatus();
    uci_accept();
}

//-----------------------------------------------------------------------------
// Disk operations
//-----------------------------------------------------------------------------

void uci_mount_disk(uint8_t id, const char *filename)
{
    int len = strlen(filename);
    uint8_t *cmd = (uint8_t *)malloc(len + 3);
    int x;
    if (!cmd) return;

    cmd[0] = 0x00;
    cmd[1] = DOS_CMD_MOUNT_DISK;
    cmd[2] = id;
    for (x = 0; x < len; x++)
        cmd[x + 3] = filename[x];

    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, len + 3);
    free(cmd);

    uci_readdata();
    uci_readstatus();
    uci_accept();
}

void uci_unmount_disk(uint8_t id)
{
    uint8_t cmd[] = {0x00, DOS_CMD_UMOUNT_DISK, id};
    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, 3);
    uci_readdata();
    uci_readstatus();
    uci_accept();
}

void uci_swap_disk(uint8_t id)
{
    uint8_t cmd[] = {0x00, DOS_CMD_SWAP_DISK, id};
    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, 3);
    uci_readdata();
    uci_readstatus();
    uci_accept();
}

//-----------------------------------------------------------------------------
// Network - basic
//-----------------------------------------------------------------------------

void uci_getinterfacecount(void)
{
    uint8_t saved = uci_target;
    uint8_t cmd[] = {0x00, NET_CMD_GET_INTERFACE_COUNT};

    uci_settarget(UCI_TARGET_NETWORK);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();

    uci_target = saved;
}

void uci_getipaddress(void)
{
    uint8_t saved = uci_target;
    uint8_t cmd[] = {0x00, NET_CMD_GET_IP_ADDRESS, 0x00};

    uci_settarget(UCI_TARGET_NETWORK);
    uci_sendcommand(cmd, 3);
    uci_readdata();
    uci_readstatus();
    uci_accept();

    uci_target = saved;
}

//-----------------------------------------------------------------------------
// Network - TCP/UDP connections
//-----------------------------------------------------------------------------

static uint8_t uci_connect(const char *host, uint16_t port, uint8_t netcmd)
{
    uint8_t saved = uci_target;
    int len = strlen(host);
    uint8_t *cmd = (uint8_t *)malloc(len + 5);
    int x;
    if (!cmd) return 0;

    cmd[0] = 0x00;
    cmd[1] = netcmd;
    cmd[2] = port & 0xFF;
    cmd[3] = (port >> 8) & 0xFF;
    for (x = 0; x < len; x++)
        cmd[x + 4] = host[x];
    cmd[len + 4] = 0x00;

    uci_settarget(UCI_TARGET_NETWORK);
    uci_sendcommand(cmd, len + 5);
    free(cmd);

    uci_readdata();
    uci_readstatus();
    uci_accept();

    uci_target = saved;
    uci_data_index = 0;
    uci_data_len = 0;

    return uci_data[0];
}

uint8_t uci_tcp_connect(const char *host, uint16_t port)
{
    return uci_connect(host, port, NET_CMD_TCP_SOCKET_CONNECT);
}

uint8_t uci_udp_connect(const char *host, uint16_t port)
{
    return uci_connect(host, port, NET_CMD_UDP_SOCKET_CONNECT);
}

void uci_socket_close(uint8_t socketid)
{
    uint8_t saved = uci_target;
    uint8_t cmd[] = {0x00, NET_CMD_SOCKET_CLOSE, socketid};

    uci_settarget(UCI_TARGET_NETWORK);
    uci_sendcommand(cmd, 3);
    uci_readdata();
    uci_readstatus();
    uci_accept();

    uci_target = saved;
}

int uci_socket_read(uint8_t socketid, uint16_t length)
{
    uint8_t saved = uci_target;
    uint8_t cmd[] = {0x00, NET_CMD_SOCKET_READ, socketid,
                     (uint8_t)(length & 0xFF), (uint8_t)((length >> 8) & 0xFF)};

    uci_settarget(UCI_TARGET_NETWORK);
    uci_sendcommand(cmd, 5);
    uci_readdata();
    uci_readstatus();
    uci_accept();

    uci_target = saved;
    return uci_data[0] | (uci_data[1] << 8);
}

// Static buffer for socket writes - avoids malloc/free per call
static uint8_t write_cmd[192];

static void uci_socket_write_internal(uint8_t socketid, const char *data, bool ascii)
{
    uint8_t saved = uci_target;
    int len = strlen(data);
    int x;
    char c;

    // Clamp to buffer size (192 - 3 header bytes)
    if (len > 189)
        len = 189;

    write_cmd[0] = 0x00;
    write_cmd[1] = NET_CMD_SOCKET_WRITE;
    write_cmd[2] = socketid;

    for (x = 0; x < len; x++)
    {
        c = data[x];
        if (ascii)
        {
            // Convert PETSCII to ASCII
            if ((c >= 97 && c <= 122) || (c >= 193 && c <= 218))
                c &= 95;
            else if (c >= 65 && c <= 90)
                c |= 32;
            else if (c == 13)
                c = 10;
        }
        write_cmd[x + 3] = c;
    }

    uci_settarget(UCI_TARGET_NETWORK);
    uci_sendcommand(write_cmd, len + 3);

    uci_readdata();
    uci_readstatus();
    uci_accept();

    uci_target = saved;
    uci_data_index = 0;
    uci_data_len = 0;
}

void uci_socket_write(uint8_t socketid, const char *data)
{
    uci_socket_write_internal(socketid, data, false);
}

void uci_socket_write_ascii(uint8_t socketid, const char *data)
{
    uci_socket_write_internal(socketid, data, true);
}

void uci_socket_write_char(uint8_t socketid, char c)
{
    temp_char[0] = c;
    temp_char[1] = 0;
    uci_socket_write(socketid, temp_char);
}

//-----------------------------------------------------------------------------
// Network - TCP listener
//-----------------------------------------------------------------------------

int uci_tcp_listen_start(uint16_t port)
{
    uint8_t saved = uci_target;
    uint8_t cmd[] = {0x00, NET_CMD_TCP_LISTENER_START,
                     (uint8_t)(port & 0xFF), (uint8_t)((port >> 8) & 0xFF)};

    uci_settarget(UCI_TARGET_NETWORK);
    uci_sendcommand(cmd, 4);
    uci_readdata();
    uci_readstatus();
    uci_accept();

    uci_target = saved;
    return uci_data[0] | (uci_data[1] << 8);
}

int uci_tcp_listen_stop(void)
{
    uint8_t saved = uci_target;
    uint8_t cmd[] = {0x00, NET_CMD_TCP_LISTENER_STOP};

    uci_settarget(UCI_TARGET_NETWORK);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();

    uci_target = saved;
    return uci_data[0] | (uci_data[1] << 8);
}

int uci_tcp_get_listen_state(void)
{
    uint8_t saved = uci_target;
    uint8_t cmd[] = {0x00, NET_CMD_GET_LISTENER_STATE};

    uci_settarget(UCI_TARGET_NETWORK);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();

    uci_target = saved;
    return uci_data[0] | (uci_data[1] << 8);
}

uint8_t uci_tcp_get_listen_socket(void)
{
    uint8_t saved = uci_target;
    uint8_t cmd[] = {0x00, NET_CMD_GET_LISTENER_SOCKET};

    uci_settarget(UCI_TARGET_NETWORK);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();

    uci_target = saved;
    return uci_data[0];
}

//-----------------------------------------------------------------------------
// Network - convenience read functions
//-----------------------------------------------------------------------------

// Silent-server timeout for uci_tcp_nextchar, in seconds of wall time.
// It is time, not a retry count: an empty READ costs whatever the firmware
// makes it cost (about 40 ms on 3.12a-3.15, a few ms where READ does not
// wait), so 500 retries meant anything from ~20 s down to ~1.6 s.
#define UCI_READ_TIMEOUT_SECS 20

// CIA1 time-of-day clock. It counts the 50/60 Hz mains tick and needs no
// IRQ, so it also runs under the cart targets, where the KERNAL jiffy
// clock at $A2 is frozen (see main.c).
#define CIA1_TOD_10THS (*(volatile uint8_t *)0xDC08)
#define CIA1_TOD_SEC   (*(volatile uint8_t *)0xDC09)
#define CIA1_CRB       (*(volatile uint8_t *)0xDC0F)

// TOD seconds are BCD
#define TOD_BIN(x) ((((x) >> 4) << 3) + (((x) >> 4) << 1) + ((x) & 0x0F))

char uci_tcp_nextchar(uint8_t socketid)
{
    char result;

    if (uci_data_index < uci_data_len)
    {
        result = uci_data[uci_data_index + 2];
        uci_data_index++;
    }
    else
    {
        uint8_t secs = 0, last, now;

        CIA1_CRB &= 0x7F;           // TOD writes set the clock, not the alarm
        CIA1_TOD_10THS = 0;         // starts a stopped TOD; also frees a latch
        last = CIA1_TOD_SEC;
        do
        {
            uci_data_len = uci_socket_read(socketid, UCI_DATA_QUEUE_SZ - 4);
            if (uci_data_len == 0)
                return 0; // EOF
            (void)CIA1_TOD_10THS;
            now = CIA1_TOD_SEC;
            if (now != last)
            {
                // Elapsed seconds (BCD, wraps at 60), not register changes
                do
                {
                    last = (last & 0x0F) == 9 ? (last + 7) : (last + 1);
                    if (last == 0x60)
                        last = 0;
                    secs++;
                } while (last != now);
                if (secs >= UCI_READ_TIMEOUT_SECS)
                    return 0; // Timeout - connection likely dead.
            }
        } while (uci_data_len == -1);

        result = uci_data[2];
        uci_data_index = 1;
    }
    return result;
}

static int uci_tcp_nextline_internal(uint8_t socketid, char *result, int maxlen, bool swapcase)
{
    int c, count = 0;
    *result = 0;

    while ((c = uci_tcp_nextchar(socketid)) != 0 && c != 0x0A)
    {
        if (c == 0x0D)
            continue;

        if (swapcase)
        {
            if ((c >= 97 && c <= 122) || (c >= 193 && c <= 218))
                c &= 95;
            else if (c >= 65 && c <= 90)
                c |= 32;
        }
        if (count < maxlen - 1)
            result[count++] = c;
    }
    result[count] = 0;
    return c != 0 || count > 0;
}

int uci_tcp_nextline(uint8_t socketid, char *result, int maxlen)
{
    return uci_tcp_nextline_internal(socketid, result, maxlen, false);
}

int uci_tcp_nextline_ascii(uint8_t socketid, char *result, int maxlen)
{
    return uci_tcp_nextline_internal(socketid, result, maxlen, true);
}

void uci_tcp_emptybuffer(void)
{
    uci_data_index = 0;
}

void uci_reset_data(void)
{
    uci_data_len = 0;
    uci_data_index = 0;
    memset(uci_data, 0, UCI_DATA_QUEUE_SZ * 2);
    memset(uci_status, 0, UCI_STATUS_QUEUE_SZ);
}

//-----------------------------------------------------------------------------
// Drive control
//-----------------------------------------------------------------------------

void uci_pop_ultimate_menu(void)
{
    uint8_t cmd[] = {0x00, CTRL_CMD_FREEZE};
    uci_settarget(UCI_TARGET_CONTROL);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();
}

void uci_enable_drive_a(void)
{
    uint8_t cmd[] = {0x00, CTRL_CMD_ENABLE_DISK_A};
    uci_settarget(UCI_TARGET_CONTROL);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();
}

void uci_disable_drive_a(void)
{
    uint8_t cmd[] = {0x00, CTRL_CMD_DISABLE_DISK_A};
    uci_settarget(UCI_TARGET_CONTROL);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();
}

void uci_enable_drive_b(void)
{
    uint8_t cmd[] = {0x00, CTRL_CMD_ENABLE_DISK_B};
    uci_settarget(UCI_TARGET_CONTROL);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();
}

void uci_disable_drive_b(void)
{
    uint8_t cmd[] = {0x00, CTRL_CMD_DISABLE_DISK_B};
    uci_settarget(UCI_TARGET_CONTROL);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();
}

//-----------------------------------------------------------------------------
// Time
//-----------------------------------------------------------------------------

void uci_get_time(void)
{
    uint8_t cmd[] = {0x00, DOS_CMD_GET_TIME};
    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();
}

void uci_set_time(const char *data)
{
    uint8_t cmd[8];
    int x;

    cmd[0] = 0x00;
    cmd[1] = DOS_CMD_SET_TIME;
    for (x = 0; x < 6; x++)
        cmd[x + 2] = data[x];

    uci_settarget(UCI_TARGET_DOS1);
    uci_sendcommand(cmd, 8);
    uci_readstatus();
    uci_accept();
}

//-----------------------------------------------------------------------------
// Control
//-----------------------------------------------------------------------------

void uci_freeze(void)
{
    uint8_t cmd[] = {0x00, 0x05};
    uci_settarget(UCI_TARGET_CONTROL);
    uci_sendcommand(cmd, 2);
    uci_readdata();
    uci_readstatus();
    uci_accept();
}
