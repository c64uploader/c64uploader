// Minimal UCI diagnostic — captures raw register state before, during, and
// after a hand-crafted DOS_CMD_IDENTIFY. Result lands in $C000+ for the host
// to peek via `c64uploader debug peek 0xC000 256`. No screen output, no UCI
// library — bypasses everything except the bare register accesses.
//
// Layout of the diagnostic buffer at $C000:
//   $C000  : 0xAA sentinel (run completed at all)
//   $C001-$C004 : initial $DE1C-$DE1F (status, id, resp, status_data)
//   $C005-$C008 : initial $DF1C-$DF1F
//   $C010-$C03F : 48-byte $DE1C status trace during/after IDENTIFY attempt
//   $C040-$C06F : 48-byte $DF1C status trace during/after IDENTIFY attempt
//   $C070-$C07F : 16-byte $DE1F (status_data queue) drain dump
//   $C080-$C08F : 16-byte $DE1E (resp_data queue) drain dump
//   $C090-$C09F : same for $DF1F status_data
//   $C0A0-$C0AF : same for $DF1E resp_data
//   $C0FF       : 0xEE end sentinel

#include <stdint.h>

#define DIAG ((volatile uint8_t *)0xC000)

#define DE_STATUS ((volatile uint8_t *)0xDE1C)
#define DE_CMD    ((volatile uint8_t *)0xDE1D)
#define DE_RESP   ((volatile uint8_t *)0xDE1E)
#define DE_STAT   ((volatile uint8_t *)0xDE1F)
#define DE_CTRL   ((volatile uint8_t *)0xDE1C)

#define DF_STATUS ((volatile uint8_t *)0xDF1C)
#define DF_CMD    ((volatile uint8_t *)0xDF1D)
#define DF_RESP   ((volatile uint8_t *)0xDF1E)
#define DF_STAT   ((volatile uint8_t *)0xDF1F)
#define DF_CTRL   ((volatile uint8_t *)0xDF1C)

#define UCI_CTRL_PUSH_CMD 0x01
#define UCI_CTRL_DATA_ACC 0x02
#define UCI_CTRL_ABORT    0x04
#define UCI_CTRL_CLR_ERR  0x08

#define UCI_TARGET_DOS1  0x01
#define DOS_CMD_IDENTIFY 0x01

static void clear_buf(void)
{
    uint8_t i;
    for (i = 0; i < 255; i++) DIAG[i] = 0x00;
    DIAG[255] = 0x00;
}

static void capture_initial(void)
{
    DIAG[0x01] = *DE_STATUS;
    DIAG[0x02] = *DE_CMD;
    DIAG[0x03] = *DE_RESP;
    DIAG[0x04] = *DE_STAT;
    DIAG[0x05] = *DF_STATUS;
    DIAG[0x06] = *DF_CMD;
    DIAG[0x07] = *DF_RESP;
    DIAG[0x08] = *DF_STAT;
}

static void try_window_de(void)
{
    uint8_t i;
    // Trace slot 0: status before any writes
    DIAG[0x10] = *DE_STATUS;

    // Drain anything stale: abort, then quick reads of resp/stat queues.
    *DE_CTRL = UCI_CTRL_ABORT;
    for (i = 0; i < 8; i++) {
        (void)*DE_RESP;
        (void)*DE_STAT;
    }
    DIAG[0x11] = *DE_STATUS;     // after abort+drain
    *DE_CTRL = UCI_CTRL_CLR_ERR;
    DIAG[0x12] = *DE_STATUS;     // after clear-err

    // Hand-craft the IDENTIFY: write target, write cmd, push.
    *DE_CMD = UCI_TARGET_DOS1;
    DIAG[0x13] = *DE_STATUS;
    *DE_CMD = DOS_CMD_IDENTIFY;
    DIAG[0x14] = *DE_STATUS;
    *DE_CTRL = UCI_CTRL_PUSH_CMD;
    DIAG[0x15] = *DE_STATUS;     // immediately after push

    // Sample status many times — hopefully we see DATA_AV (0x80) or STAT_AV
    // (0x40) flip on if the firmware is processing the command.
    for (i = 0; i < 40; i++) {
        DIAG[0x16 + i] = *DE_STATUS;
    }

    // Drain whatever the queues hold.
    for (i = 0; i < 16; i++) DIAG[0x70 + i] = *DE_STAT;
    for (i = 0; i < 16; i++) DIAG[0x80 + i] = *DE_RESP;

    // Acknowledge data, see if status changes.
    *DE_CTRL = UCI_CTRL_DATA_ACC;
    DIAG[0x3F] = *DE_STATUS;
}

static void try_window_df(void)
{
    uint8_t i;
    DIAG[0x40] = *DF_STATUS;

    *DF_CTRL = UCI_CTRL_ABORT;
    for (i = 0; i < 8; i++) {
        (void)*DF_RESP;
        (void)*DF_STAT;
    }
    DIAG[0x41] = *DF_STATUS;
    *DF_CTRL = UCI_CTRL_CLR_ERR;
    DIAG[0x42] = *DF_STATUS;

    *DF_CMD = UCI_TARGET_DOS1;
    DIAG[0x43] = *DF_STATUS;
    *DF_CMD = DOS_CMD_IDENTIFY;
    DIAG[0x44] = *DF_STATUS;
    *DF_CTRL = UCI_CTRL_PUSH_CMD;
    DIAG[0x45] = *DF_STATUS;

    for (i = 0; i < 40; i++) {
        DIAG[0x46 + i] = *DF_STATUS;
    }

    for (i = 0; i < 16; i++) DIAG[0x90 + i] = *DF_STAT;
    for (i = 0; i < 16; i++) DIAG[0xA0 + i] = *DF_RESP;

    *DF_CTRL = UCI_CTRL_DATA_ACC;
    DIAG[0x6F] = *DF_STATUS;
}

int main(void)
{
    clear_buf();
    DIAG[0x00] = 0xAA;     // start sentinel — set early so we know main ran
    capture_initial();
    try_window_de();
    try_window_df();
    DIAG[0xFF] = 0xEE;     // end sentinel — confirms full run

    // Halt forever so the C64 doesn't run off into BASIC and trash $C000.
    for (;;) { }
    return 0;
}
