// Remote control client for the C64 Ultimate.
// It supports uploading and running various file formats including PRG, CRT, and D64 disk images.
// The application can run in either CLI mode (single file upload) or TUI mode (interactive browser for Assembly64).
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// isURL checks if a string is a URL.
func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// downloadURL downloads a remote file to memory.
func downloadURL(url string) ([]byte, error) {
	slog.Info("Downloading remote file", "url", url)
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("downloading URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed with status: %s", resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	slog.Info("Download complete", "size", len(data))
	return data, nil
}

// detectFileType determines the file type from extension.
func detectFileType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".prg":
		return "prg"
	case ".crt":
		return "crt"
	case ".d64":
		return "d64"
	case ".d71":
		return "d71"
	case ".d81":
		return "d81"
	case ".g64":
		return "g64"
	case ".g71":
		return "g71"
	case ".sid":
		return "sid"
	default:
		return ""
	}
}

// uploadAndRunFile uploads a file and runs it based on file type.
func uploadAndRunFile(client *APIClient, fileData []byte, filename string) error {
	// Detect file type.
	fileType := detectFileType(filename)
	if fileType == "" {
		return fmt.Errorf("unsupported file type (supported: .prg, .crt, .d64, .d71, .d81, .g64, .g71, .sid)")
	}

	slog.Info("Detected file type", "type", fileType)
	// Upload and run based on type.
	switch fileType {
	case "prg":
		return client.runPRG(fileData)
	case "crt":
		return client.runCRT(fileData)
	case "d64", "d71", "d81", "g64", "g71":
		return client.runDiskImage(fileData, fileType, filepath.Base(filename))
	case "sid":
		return client.runSID(fileData)
	default:
		return fmt.Errorf("unsupported file type: %s", fileType)
	}
}

// parsePoke parses a POKE address and value.
func parsePoke(addressStr, valueStr string) (string, byte, error) {
	// Parse address.
	// C64 Ultimate API expects hex address without 0x or $.
	// User might provide 53280, 0xD020, $D020.
	var address int
	var err error

	// Handle $ prefix for hex.
	if strings.HasPrefix(addressStr, "$") {
		addressStr = "0x" + addressStr[1:]
	}

	// Try parsing. auto-detect base (0x for hex, else decimal).
	_, err = fmt.Sscanf(addressStr, "%v", &address)
	if err != nil {
		// Re-attempt as hex if initial parse failed (e.g. "D020")
		_, errHex := fmt.Sscanf(addressStr, "%x", &address)
		if errHex != nil {
			return "", 0, fmt.Errorf("invalid address '%s': %v", addressStr, err)
		}
	}

	// Format address as hex string for API (without 0x).
	addressHex := fmt.Sprintf("%x", address)

	// Parse value.
	var value int
	// Handle $ prefix
	if strings.HasPrefix(valueStr, "$") {
		valueStr = "0x" + valueStr[1:]
	}
	_, err = fmt.Sscanf(valueStr, "%v", &value)
	if err != nil {
		// Re-attempt as hex (e.g. "FF")
		_, errHex := fmt.Sscanf(valueStr, "%x", &value)
		if errHex != nil {
			return "", 0, fmt.Errorf("invalid value '%s': %v", valueStr, err)
		}
	}

	if value < 0 || value > 255 {
		return "", 0, fmt.Errorf("value must be 0-255 (byte), got %d", value)
	}

	return addressHex, byte(value), nil
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: c64uploader <command> [options] [arguments]\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  tui                       Launch the Terminal UI browser\n")
	fmt.Fprintf(os.Stderr, "  load <filename>           Upload and run a file (PRG, CRT, D64, etc.)\n")
	fmt.Fprintf(os.Stderr, "  ftp <filename> <dest>     Upload a file via FTP to C64 Ultimate\n")
	fmt.Fprintf(os.Stderr, "  poke <address>,<value>    Issue a POKE command to C64 memory\n")
	fmt.Fprintf(os.Stderr, "  server                    Start the C64 protocol server (SQLite)\n")
	fmt.Fprintf(os.Stderr, "  sqlitegen                 Generate SQLite database from Assembly64\n")
	fmt.Fprintf(os.Stderr, "  debug <sub>               Remote debug: screen, press, reset, reboot, menu, info\n\n")
	fmt.Fprintf(os.Stderr, "Run 'c64uploader <command> -help' for command-specific options.\n")
}

// loadIndex loads the search index, trying SQLite first, then legacy.
// assembly64Path should already be expanded (no ~).
func loadIndex(assembly64Path string, forceLegacy bool) (*SearchIndex, error) {
	if forceLegacy {
		// Fall back to legacy .releaselog.json loading.
		return loadAssembly64Index(assembly64Path)
	}

	// Try SQLite database first.
	dbPath := filepath.Join(assembly64Path, "c64uploader.db")
	if _, err := os.Stat(dbPath); err == nil {
		slog.Info("Found SQLite database", "path", dbPath)
		return LoadIndexFromSQLite(dbPath, assembly64Path)
	}

	// Fall back to legacy .releaselog.json loading.
	slog.Info("No SQLite database found, falling back to legacy loading")
	return loadAssembly64Index(assembly64Path)
}

func runTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	host := fs.String("host", "c64u", "C64 Ultimate hostname or IP address")
	verbose := fs.Bool("v", false, "Enable verbose debug logging")
	assembly64Path := fs.String("assembly64", "~/Downloads/assembly64", "Path to Assembly64 data directory")
	legacy := fs.Bool("legacy", false, "Force legacy .releaselog.json loading")
	fs.Parse(args)

	// Set log level.
	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	// Disable slog output in TUI mode to avoid interfering with the display.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Expand assembly64Path for TUI (needed for file operations).
	a64Path := *assembly64Path
	if strings.HasPrefix(a64Path, "~/") {
		home, _ := os.UserHomeDir()
		a64Path = filepath.Join(home, a64Path[2:])
	}

	index, err := loadIndex(a64Path, *legacy)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to load index: %v\n", err)
		os.Exit(1)
	}

	// Determine if we're in legacy mode (no SQLite database or -legacy flag).
	legacyMode := *legacy
	if !legacyMode {
		// Check if SQLite database exists.
		dbPath := filepath.Join(a64Path, "c64uploader.db")
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			legacyMode = true
		}
	}

	// Create API client.
	client := NewAPIClient(*host)

	// Launch TUI.
	p := tea.NewProgram(NewModel(index, client, a64Path, legacyMode), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}

func runLoad(args []string) {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	host := fs.String("host", "c64u", "C64 Ultimate hostname or IP address")
	verbose := fs.Bool("v", false, "Enable verbose debug logging")
	fs.Parse(args)

	// Set log level.
	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: filename or URL required\n")
		fmt.Fprintf(os.Stderr, "Usage: c64uploader load [options] <filename|url>\n")
		os.Exit(1)
	}

	input := fs.Arg(0)

	// Load data from URL or local file.
	var fileData []byte
	var err error

	if isURL(input) {
		slog.Info("Detected URL", "url", input)
		fileData, err = downloadURL(input)
		if err != nil {
			slog.Error("Failed to download URL", "error", err)
			os.Exit(1)
		}
	} else {
		slog.Info("Reading local file", "path", input)
		fileData, err = os.ReadFile(input)
		if err != nil {
			slog.Error("Failed to read file", "error", err)
			os.Exit(1)
		}
	}

	// Create API client.
	client := NewAPIClient(*host)

	slog.Info("Connecting to C64 Ultimate", "host", *host)
	slog.Info("Uploading file", "path", input, "size", len(fileData))

	if err := uploadAndRunFile(client, fileData, input); err != nil {
		slog.Error("Failed to upload and run file", "error", err)
		os.Exit(1)
	}

	slog.Info("Success! Program uploaded and running")
}

func runPoke(args []string) {
	fs := flag.NewFlagSet("poke", flag.ExitOnError)
	host := fs.String("host", "c64u", "C64 Ultimate hostname or IP address")
	verbose := fs.Bool("v", false, "Enable verbose debug logging")
	fs.Parse(args)

	// Set log level.
	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: address and value required\n")
		fmt.Fprintf(os.Stderr, "Usage: c64uploader poke [options] <address>,<value>\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  c64uploader poke 53280,0      # Decimal address and value\n")
		fmt.Fprintf(os.Stderr, "  c64uploader poke 0xD020,0     # Hex address (0x prefix)\n")
		fmt.Fprintf(os.Stderr, "  c64uploader poke $D020,0      # Hex address ($ prefix)\n")
		fmt.Fprintf(os.Stderr, "  c64uploader poke D020,0       # Hex address (no prefix)\n")
		os.Exit(1)
	}

	// Join all arguments to handle "poke address, value" vs "poke address,value" etc.
	rawArgs := strings.Join(fs.Args(), "")
	parts := strings.Split(rawArgs, ",")

	if len(parts) != 2 {
		fmt.Fprintln(os.Stderr, "Error: Invalid format. Use: <address>,<value>")
		os.Exit(1)
	}

	addressStr := strings.TrimSpace(parts[0])
	valueStr := strings.TrimSpace(parts[1])

	addressHex, value, err := parsePoke(addressStr, valueStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Create API client.
	client := NewAPIClient(*host)

	if err := client.WriteMemory(addressHex, []byte{value}); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to poke: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("POKE %s,%d OK\n", addressStr, value)
}

func runFTP(args []string) {
	fs := flag.NewFlagSet("ftp", flag.ExitOnError)
	host := fs.String("host", "c64u", "C64 Ultimate hostname or IP address")
	verbose := fs.Bool("v", false, "Enable verbose debug logging")
	fs.Parse(args)

	// Set log level.
	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	if fs.NArg() < 2 {
		fmt.Fprintf(os.Stderr, "Error: filename/URL and destination required\n")
		fmt.Fprintf(os.Stderr, "Usage: c64uploader ftp [options] <filename|url> <destination>\n")
		fmt.Fprintf(os.Stderr, "Example: c64uploader ftp ~/games/game.prg /Temp/game.prg\n")
		fmt.Fprintf(os.Stderr, "Example: c64uploader ftp https://example.com/game.prg /Temp/game.prg\n")
		os.Exit(1)
	}

	input := fs.Arg(0)
	destination := fs.Arg(1)

	// Load data from URL or local file.
	var fileData []byte
	var err error

	if isURL(input) {
		slog.Info("Detected URL", "url", input)
		fileData, err = downloadURL(input)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error downloading URL: %v\n", err)
			os.Exit(1)
		}
	} else {
		slog.Info("Reading local file", "path", input)
		fileData, err = os.ReadFile(input)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
			os.Exit(1)
		}
	}

	// Create API client.
	client := NewAPIClient(*host)

	slog.Info("Connecting to C64 Ultimate FTP server", "host", *host)
	slog.Info("Uploading file", "source", input, "destination", destination, "size", len(fileData))

	// Use the existing FTP upload method but with custom destination.
	ftpAddr := fmt.Sprintf("%s:21", *host)
	conn, err := client.ftpConnect(ftpAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to FTP server: %v\n", err)
		os.Exit(1)
	}
	defer conn.Quit()

	if err := client.ftpUpload(conn, fileData, destination); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to upload file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("File uploaded successfully to %s\n", destination)
}

func runServer(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	host := fs.String("host", "c64u", "C64 Ultimate hostname or IP address")
	verbose := fs.Bool("v", false, "Enable verbose debug logging")
	assembly64Path := fs.String("assembly64", "~/Downloads/assembly64", "Path to Assembly64 data directory")
	myfilesPath := fs.String("myfiles", "", "Path to the My Files directory exposed to the C64 client (default: <assembly64>/Myfiles)")
	port := fs.Int("port", 6465, "C64 protocol server port")
	spiffyPort := fs.Int("spiffy-http-port", 0, "Spiffy-compatible HTTP API port (0 = disabled). Lets the Ultimate firmware's built-in Assembly64 browser use this server as its upstream.")
	spiffyClientID := fs.String("spiffy-client-id", "", "Optional Client-Id header value the Spiffy HTTP server requires (default: unauthenticated).")
	reindexInterval := fs.Duration("reindex-interval", 0, "Poll the Assembly64 directory this often and hot-swap a rebuilt database when files are added/changed (0 = disabled).")
	fs.Parse(args)

	// Set log level.
	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	// Expand assembly64Path for server (needed for file operations).
	a64Path := *assembly64Path
	if strings.HasPrefix(a64Path, "~/") {
		home, _ := os.UserHomeDir()
		a64Path = filepath.Join(home, a64Path[2:])
	}

	// Resolve the My Files directory: explicit -myfiles wins, otherwise default
	// to <assembly64>/Myfiles so a single -assembly64 flag covers the common case.
	mfPath := *myfilesPath
	if mfPath == "" {
		mfPath = filepath.Join(a64Path, "Myfiles")
	} else if strings.HasPrefix(mfPath, "~/") {
		home, _ := os.UserHomeDir()
		mfPath = filepath.Join(home, mfPath[2:])
	}

	// Check if SQLite database exists.
	dbPath := filepath.Join(a64Path, "c64uploader.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: SQLite database not found: %s\n", dbPath)
		fmt.Fprintf(os.Stderr, "Generate it with: c64uploader sqlitegen -assembly64 %s\n", a64Path)
		os.Exit(1)
	}

	// Create API client.
	apiClient := NewAPIClient(*host)

	// Use SQLite database.
	slog.Info("Using SQLite database", "path", dbPath)
	fmt.Printf("Using SQLite database: %s\n", dbPath)

	sqliteServer, err := NewSQLiteServer(dbPath, a64Path, mfPath)
	if err != nil {
		slog.Error("Failed to open SQLite database", "error", err)
		os.Exit(1)
	}
	defer sqliteServer.Close()

	// Optional auto-reindex: poll the Assembly64 directory and hot-swap a freshly
	// rebuilt database when files are added/changed (NFS-friendly, no inotify).
	if *reindexInterval > 0 {
		go sqliteServer.autoReindexLoop(*reindexInterval, a64Path, dbPath)
	}

	// Optional Spiffy-compatible HTTP listener for the Ultimate firmware's
	// built-in Assembly64 browser. Backed by the same SQLite handle as the
	// TCP line server. See uploader/SPIFFY_HTTP_API.md.
	if *spiffyPort > 0 {
		go func(p int, cid string) {
			if err := startSpiffyHTTP(p, sqliteServer, apiClient, a64Path, cid); err != nil {
				slog.Error("Spiffy HTTP server error", "error", err)
			}
		}(*spiffyPort, *spiffyClientID)
	}

	// Start C64 protocol server with SQLite backend.
	if err := StartC64ServerSQLite(*port, sqliteServer, apiClient, a64Path); err != nil {
		slog.Error("C64 server error", "error", err)
		os.Exit(1)
	}

	// Block forever
	select {}
}

func runSQLiteGen(args []string) {
	fs := flag.NewFlagSet("sqlitegen", flag.ExitOnError)
	assembly64Path := fs.String("assembly64", "", "Path to Assembly64 data directory (required)")
	outputFile := fs.String("output", "", "Output SQLite database file (default: <assembly64>/c64uploader.db)")
	fs.Parse(args)

	if *assembly64Path == "" {
		fmt.Fprintf(os.Stderr, "Error: -assembly64 path is required\n")
		fmt.Fprintf(os.Stderr, "Usage: c64uploader sqlitegen -assembly64 <path> [-output <file>]\n")
		fmt.Fprintf(os.Stderr, "Example: c64uploader sqlitegen -assembly64 ~/assembly64\n")
		os.Exit(1)
	}

	// Expand ~ to home directory.
	path := *assembly64Path
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to get home directory: %v\n", err)
			os.Exit(1)
		}
		path = filepath.Join(home, path[2:])
	}

	// Check if path exists.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: path does not exist: %s\n", path)
		os.Exit(1)
	}

	// Determine output file.
	dbFile := *outputFile
	if dbFile == "" {
		dbFile = filepath.Join(path, "c64uploader.db")
	}

	// Generate SQLite database.
	if err := GenerateSQLiteDB(path, dbFile); err != nil {
		fmt.Fprintf(os.Stderr, "Error generating SQLite database: %v\n", err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "tui":
		runTUI(os.Args[2:])
	case "load":
		runLoad(os.Args[2:])
	case "ftp":
		runFTP(os.Args[2:])
	case "poke":
		runPoke(os.Args[2:])
	case "server":
		runServer(os.Args[2:])
	case "sqlitegen":
		runSQLiteGen(os.Args[2:])
	case "debug":
		runDebug(os.Args[2:])
	case "-h", "-help", "--help", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown command '%s'\n\n", command)
		printUsage()
		os.Exit(1)
	}
}
