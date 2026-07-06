# Assembly64 Browser - C64 Client
# Oscar64 Makefile

# Oscar64 compiler path (adjust if needed)
OSCAR64 = oscar64

# Oscar64 include path - set if not installed system-wide
# Example: OSCAR64_INCLUDE = /home/user/oscar64/include
OSCAR64_INCLUDE ?=

# Project settings
PROJECT = a64browser
SRCDIR = src
OUTDIR = build

# Source files
SOURCES = $(SRCDIR)/main.c $(SRCDIR)/ultimate.c

# Output files
PRG = $(OUTDIR)/$(PROJECT).prg
CRT = $(OUTDIR)/$(PROJECT).crt
D64 = $(OUTDIR)/$(PROJECT).d64

# Compiler flags
# -n  = native code (faster)
# -O2 = optimization level 2
# -Oo = outliner: factor repeated code sequences into shared subroutines (~1.4KB)
# -Oz = auto-place globals in zero page (smaller + faster)
# -tm = target machine (c64)
# -dNOFLOAT = omit float formatting from printf — we never format floats and
#             this strips ~1.6KB of unused nformf / faddsub / crt_fdiv code.
# -Oo/-Oz are what keep the EasyFlash .crt's resident image inside the single
# 16 KB bank-0 ROM. Multi-bank EasyFlash is NOT viable on the C64 Ultimate's
# cart emulation (see docs/architecture-c64client.md "Why not multi-bank"), so
# the binary must fit one bank; these size optimizations are load-bearing.
CFLAGS = -n -O2 -Oo -Oz -tm=c64 -dNOFLOAT

# Add include path if specified
ifneq ($(OSCAR64_INCLUDE),)
CFLAGS += -i=$(OSCAR64_INCLUDE)
endif

# EasyFlash subtype 1 ("REU-aware EasyFlash") — the cart format we ship.
# Subtype 1 is required so the Ultimate firmware leaves $DF00-$DF1F free for
# the UCI registers; subtype 0 would auto-disable UCI for the duration of the
# cart and the browser's uci_identify() would hang. The matching
# `#pragma region(main 0x0900-0x8000)` in main.c is conditional on
# OSCAR_TARGET_CRT_EASYFLASH and stretches the RAM region to 30 KB so the
# ~17 KB image fits. The vanilla 16 KB CRT16 slot can't hold the binary
# anymore, so this is the only cart target we build.
CRTFLAGS = -tf=crt -csub=1

.PHONY: all clean prg crt d64

all: prg

# Create output directory
$(OUTDIR):
	mkdir -p $(OUTDIR)

# Build PRG file
prg: $(OUTDIR) $(SOURCES)
	$(OSCAR64) $(CFLAGS) -o=$(PRG) $(SOURCES)

# Build EasyFlash cartridge (REU-aware subtype 1) — the cart we ship
crt: $(OUTDIR) $(SOURCES)
	$(OSCAR64) $(CFLAGS) $(CRTFLAGS) -o=$(CRT) $(SOURCES)

# Build D64 disk image (requires c1541 from VICE)
d64: prg
	c1541 -format "a64browser,ab" d64 $(D64) -write $(PRG) $(PROJECT)

clean:
	rm -rf $(OUTDIR)

# Run in VICE (x64sc)
run: prg
	x64sc $(PRG)

# Deploy to Ultimate II+ via FTP
# Set U2P_HOST environment variable to your Ultimate's IP
deploy: prg
	@if [ -z "$$U2P_HOST" ]; then echo "Set U2P_HOST to your Ultimate IP"; exit 1; fi
	curl -T $(PRG) ftp://$$U2P_HOST/Usb0/$(PROJECT).prg

# Run CRT directly on Ultimate II+ via HTTP API
runcrt: crt
	@if [ -z "$$U2P_HOST" ]; then echo "Set U2P_HOST to your Ultimate IP"; exit 1; fi
	curl -X POST --data-binary @$(CRT) "http://$$U2P_HOST/v1/runners:run_crt"

# Run PRG directly on Ultimate II+ via HTTP API
runprg: prg
	@if [ -z "$$U2P_HOST" ]; then echo "Set U2P_HOST to your Ultimate IP"; exit 1; fi
	curl -X POST --data-binary @$(PRG) "http://$$U2P_HOST/v1/runners:run_prg"

help:
	@echo "Assembly64 Browser - C64 Client"
	@echo ""
	@echo "Targets:"
	@echo "  prg     - Build PRG file (default)"
	@echo "  crt     - Build EasyFlash cartridge (REU-aware, subtype 1)"
	@echo "  d64     - Build D64 disk image"
	@echo "  run     - Run in VICE emulator"
	@echo "  runprg  - Run .prg directly on Ultimate via HTTP API"
	@echo "  runcrt  - Run .crt directly on Ultimate via HTTP API"
	@echo "  deploy  - Upload .prg to Ultimate via FTP"
	@echo "  clean   - Remove build files"
	@echo ""
	@echo "Requirements:"
	@echo "  - oscar64 compiler in PATH"
	@echo "  - c1541 (VICE) for d64 target"
	@echo "  - x64sc (VICE) for run target"
