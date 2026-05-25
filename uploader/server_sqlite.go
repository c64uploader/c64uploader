// SQLite-based server handlers for C64 protocol.
// These replace the in-memory JSON-based handlers with direct SQL queries.
package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	c64ReadTimeout = 5 * time.Minute
)

// StartC64ServerSQLite starts the C64 protocol server with SQLite backend.
func StartC64ServerSQLite(port int, sqliteServer *SQLiteServer, apiClient *APIClient, assembly64Path string) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to start C64 server: %w", err)
	}

	slog.Info("C64 protocol server listening (SQLite)", "port", port)
	fmt.Printf("C64 protocol server listening on :%d (SQLite backend)\n", port)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				slog.Error("Accept error", "error", err)
				continue
			}
			go handleC64ConnectionSQLite(conn, sqliteServer, apiClient, assembly64Path)
		}
	}()

	return nil
}

func handleC64ConnectionSQLite(conn net.Conn, server *SQLiteServer, apiClient *APIClient, assembly64Path string) {
	defer conn.Close()

	remoteAddr := conn.RemoteAddr().String()
	slog.Info("C64 client connected", "remote", remoteAddr)

	// Send greeting
	conn.Write([]byte("OK Assembly64 Browser\n"))

	reader := bufio.NewReader(conn)

	for {
		conn.SetReadDeadline(time.Now().Add(c64ReadTimeout))

		line, err := reader.ReadString('\n')
		if err != nil {
			slog.Debug("C64 client disconnected", "remote", remoteAddr, "error", err)
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		slog.Debug("C64 command", "remote", remoteAddr, "cmd", line)

		response := handleC64CommandSQLite(line, server, apiClient, assembly64Path, conn)
		if response == "QUIT" {
			conn.Write([]byte("OK Goodbye\n"))
			return
		}
		// Log response header (first line) for debugging
		if idx := strings.Index(response, "\n"); idx > 0 {
			slog.Debug("C64 response", "header", response[:idx])
		}
		conn.Write([]byte(response))
	}
}

func handleC64CommandSQLite(line string, server *SQLiteServer, apiClient *APIClient, assembly64Path string, conn net.Conn) string {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return "ERR Empty command\n"
	}

	cmd := strings.ToUpper(parts[0])

	switch cmd {
	case "CATS":
		return server.HandleMenu("")

	case "LIST":
		// LIST offset count path [letter] - List entries matching path, optionally filtered by letter
		if len(parts) < 4 {
			return "ERR Usage: LIST <offset> <count> <path> [letter]\n"
		}
		offset, _ := strconv.Atoi(parts[1])
		count, _ := strconv.Atoi(parts[2])
		path := parts[3]
		letter := ""
		if len(parts) >= 5 {
			letter = parts[4]
		}
		return server.HandleListPath(path, offset, count, letter)

	case "SEARCH":
		if len(parts) < 4 {
			return "ERR Usage: SEARCH <offset> <count> [category] <query>\n"
		}
		offset, _ := strconv.Atoi(parts[1])
		count, _ := strconv.Atoi(parts[2])
		// Check if parts[3] is a category
		category := ""
		queryStart := 3
		potentialCat := parts[3]
		knownCategories := []string{"Games", "Demos", "Music", "Intros", "Graphics", "Discmags", "All"}
		for _, cat := range knownCategories {
			if strings.EqualFold(cat, potentialCat) {
				category = cat
				queryStart = 4
				break
			}
		}
		if queryStart > len(parts) {
			return "ERR Usage: SEARCH <offset> <count> [category] <query>\n"
		}
		query := strings.Join(parts[queryStart:], " ")
		return server.HandleSearch(query, category, offset, count)

	case "INFO":
		if len(parts) < 2 {
			return "ERR Usage: INFO <id>\n"
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return "ERR Invalid ID\n"
		}
		return server.HandleInfo(id)

	case "RUN":
		if len(parts) < 2 {
			return "ERR Usage: RUN <id>\n"
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return "ERR Invalid ID\n"
		}
		filePath, err := server.GetFilePath(id)
		if err != nil {
			return fmt.Sprintf("ERR %s\n", err.Error())
		}
		// Run the file
		fileData, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Sprintf("ERR Failed to read file: %v\n", err)
		}
		if err := uploadAndRunFile(apiClient, fileData, filePath); err != nil {
			return fmt.Sprintf("ERR Failed to run: %v\n", err)
		}
		return "OK Running\n"

	case "RUNFILE":
		// Run a file from MYFILES directory by path
		if len(parts) < 2 {
			return "ERR Usage: RUNFILE <path>\n"
		}
		myPath := strings.Join(parts[1:], " ")
		// Validate MYFILES prefix
		if !strings.HasPrefix(myPath, "MYFILES/") {
			return "ERR Invalid MYFILES path\n"
		}
		// Extract relative path after MYFILES/
		relPath := strings.TrimPrefix(myPath, "MYFILES/")
		filePath := filepath.Join("./myfiles", relPath)

		// Security: Prevent path traversal attacks
		absBase, err := filepath.Abs("./myfiles")
		if err != nil {
			return "ERR Internal error\n"
		}
		absFull, err := filepath.Abs(filePath)
		if err != nil {
			return "ERR Invalid path\n"
		}
		if !strings.HasPrefix(absFull, absBase) {
			slog.Warn("RUNFILE: path traversal attempt blocked", "requested", myPath)
			return "ERR Invalid path\n"
		}

		// Validate file extension
		ext := strings.ToLower(filepath.Ext(filePath))
		if !isValidMyFilesExtension(ext) {
			return "ERR Invalid file type\n"
		}
		// Firmware/flash-patch files are transferred, never launched. Reject them
		// here so a stale client that still sends RUNFILE gets a clear hint rather
		// than an opaque "unsupported file type" from the runner.
		if isUploadOnlyExtension(ext) {
			return "ERR Use UPLOADFILE for firmware files\n"
		}

		// Run the file
		slog.Info("RUNFILE: executing file", "path", relPath)
		fileData, err := os.ReadFile(filePath)
		if err != nil {
			return "ERR File not found\n"
		}
		if err := uploadAndRunFile(apiClient, fileData, filePath); err != nil {
			return fmt.Sprintf("ERR Failed to run: %v\n", err)
		}
		return "OK Running\n"

	case "UPLOADFILE":
		// Transfer a file from MYFILES to the device via FTP without launching it.
		// Used for Ultimate firmware/flash-patch files (.ue2): the patch is applied
		// later from the Ultimate's on-device menu.
		if len(parts) < 2 {
			return "ERR Usage: UPLOADFILE <path>\n"
		}
		myPath := strings.Join(parts[1:], " ")
		// Validate MYFILES prefix
		if !strings.HasPrefix(myPath, "MYFILES/") {
			return "ERR Invalid MYFILES path\n"
		}
		// Extract relative path after MYFILES/
		relPath := strings.TrimPrefix(myPath, "MYFILES/")
		filePath := filepath.Join("./myfiles", relPath)

		// Security: Prevent path traversal attacks
		absBase, err := filepath.Abs("./myfiles")
		if err != nil {
			return "ERR Internal error\n"
		}
		absFull, err := filepath.Abs(filePath)
		if err != nil {
			return "ERR Invalid path\n"
		}
		if !strings.HasPrefix(absFull, absBase) {
			slog.Warn("UPLOADFILE: path traversal attempt blocked", "requested", myPath)
			return "ERR Invalid path\n"
		}

		// Validate file extension
		ext := strings.ToLower(filepath.Ext(filePath))
		if !isValidMyFilesExtension(ext) {
			return "ERR Invalid file type\n"
		}

		// Read and transfer the file via FTP, preserving its original name.
		slog.Info("UPLOADFILE: transferring file", "path", relPath)
		fileData, err := os.ReadFile(filePath)
		if err != nil {
			return "ERR File not found\n"
		}
		remotePath, err := apiClient.uploadToTempViaFTP(fileData, filepath.Base(filePath))
		if err != nil {
			return fmt.Sprintf("ERR Failed to upload: %v\n", err)
		}
		return fmt.Sprintf("OK Uploaded %s\n", remotePath)

	case "RELEASES":
		if len(parts) < 5 {
			return "ERR Usage: RELEASES <offset> <count> <path> <title>\n"
		}
		offset, _ := strconv.Atoi(parts[1])
		count, _ := strconv.Atoi(parts[2])
		// Path is parts[3] (e.g., "Games/CSDB/A"), title is everything after (may contain spaces)
		path := parts[3]
		title := strings.Join(parts[4:], " ")
		return server.HandleReleases(path, title, offset, count)

	case "MENU":
		path := ""
		if len(parts) >= 2 {
			path = strings.Join(parts[1:], " ")
		}
		return server.HandleMenu(path)

	case "LISTPATH":
		// LISTPATH offset count path [letter] - List entries matching path prefix (legacy, use LIST instead)
		if len(parts) < 4 {
			return "ERR Usage: LISTPATH <offset> <count> <path> [letter]\n"
		}
		offset, _ := strconv.Atoi(parts[1])
		count, _ := strconv.Atoi(parts[2])
		path := parts[3]
		letter := ""
		if len(parts) >= 5 {
			letter = parts[4]
		}
		return server.HandleListPath(path, offset, count, letter)

	case "LETTERS":
		// LETTERS path - Get letter counts for browse mode
		if len(parts) < 2 {
			return "ERR Usage: LETTERS <path>\n"
		}
		path := strings.Join(parts[1:], " ")
		return server.HandleLetters(path)

	case "QUIT":
		return "QUIT"

	default:
		return fmt.Sprintf("ERR Unknown command: %s\n", cmd)
	}
}

// SQLiteServer wraps the database for server operations.
type SQLiteServer struct {
	// db holds the live *DB. It is an atomic pointer so the auto-reindex loop
	// can hot-swap a freshly rebuilt database under in-flight readers without a
	// restart. Always read it via DB().
	db             atomic.Pointer[DB]
	assembly64Path string
	reindexMu      sync.Mutex // ensures at most one reindex runs at a time
}

// DB returns the currently active database handle.
func (s *SQLiteServer) DB() *DB {
	return s.db.Load()
}

// NewSQLiteServer creates a new SQLite-based server.
func NewSQLiteServer(dbPath, assembly64Path string) (*SQLiteServer, error) {
	db, err := OpenDB(dbPath)
	if err != nil {
		return nil, err
	}

	// Ensure myfiles directory exists for user content
	if err := os.MkdirAll("./myfiles", 0755); err != nil {
		slog.Warn("Could not create myfiles directory", "error", err)
	} else {
		slog.Debug("myfiles directory ready")
	}

	s := &SQLiteServer{assembly64Path: assembly64Path}
	s.db.Store(db)
	return s, nil
}

// Close closes the database connection.
func (s *SQLiteServer) Close() error {
	if d := s.DB(); d != nil {
		return d.Close()
	}
	return nil
}

// indexSignature is a cheap fingerprint of the Assembly64 tree used to detect
// additions between reindex polls. Both a new release folder and a new file
// inside an existing folder bump a directory's mtime, so newest-mtime plus a
// directory count catches additions and removals. It is comparable with ==.
type indexSignature struct {
	maxMTime int64 // newest directory mtime (unix seconds)
	dirCount int   // total number of directories
}

// indexCategories are the top-level Assembly64 subtrees the generator indexes.
// scanSignature walks only these — never the root — so that writing the rebuilt
// c64uploader.db / *.reindex files into the root cannot re-trigger a reindex.
var indexCategories = []string{"Games", "Demos", "Music", "Intros", "Graphics", "Discmags"}

// scanSignature computes an indexSignature over the category subtrees of root.
func scanSignature(root string) (indexSignature, error) {
	var sig indexSignature
	for _, cat := range indexCategories {
		catPath := filepath.Join(root, cat)
		if _, err := os.Stat(catPath); err != nil {
			continue // category absent on this mirror; skip
		}
		err := filepath.WalkDir(catPath, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // tolerate transient per-entry NFS errors
			}
			if !d.IsDir() {
				return nil
			}
			sig.dirCount++
			if info, ierr := d.Info(); ierr == nil {
				if m := info.ModTime().Unix(); m > sig.maxMTime {
					sig.maxMTime = m
				}
			}
			return nil
		})
		if err != nil {
			return sig, err
		}
	}
	return sig, nil
}

// reindex rebuilds the SQLite database from the Assembly64 directory into a
// temporary file and atomically hot-swaps it into the running server. At most
// one reindex runs at a time; a concurrent caller is skipped.
func (s *SQLiteServer) reindex(assembly64Path, dbPath string) error {
	if !s.reindexMu.TryLock() {
		slog.Debug("reindex already in progress; skipping")
		return nil
	}
	defer s.reindexMu.Unlock()

	tmp := dbPath + ".reindex"
	if err := GenerateSQLiteDB(assembly64Path, tmp); err != nil {
		return fmt.Errorf("rebuilding database: %w", err)
	}
	// Atomic replace (same directory/filesystem). The old open handle keeps the
	// old inode alive until it is closed below; on NFS this is a silly-rename.
	if err := os.Rename(tmp, dbPath); err != nil {
		return fmt.Errorf("swapping database file: %w", err)
	}
	newDB, err := OpenDB(dbPath)
	if err != nil {
		return fmt.Errorf("opening rebuilt database: %w", err)
	}
	old := s.db.Swap(newDB)
	// Close the previous handle after a grace period so in-flight queries on it
	// finish first.
	if old != nil {
		time.AfterFunc(60*time.Second, func() {
			if cerr := old.Close(); cerr != nil {
				slog.Debug("closing previous DB handle", "error", cerr)
			}
		})
	}
	return nil
}

// autoReindexLoop periodically checks the Assembly64 directory for added or
// changed files and, when it detects a change, rebuilds and hot-swaps the DB.
// Intended to run in its own goroutine for the server's lifetime.
func (s *SQLiteServer) autoReindexLoop(interval time.Duration, assembly64Path, dbPath string) {
	last, err := scanSignature(assembly64Path)
	if err != nil {
		slog.Warn("auto-reindex: initial scan failed", "error", err)
	}
	slog.Info("auto-reindex enabled", "interval", interval, "dirs", last.dirCount)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		sig, serr := scanSignature(assembly64Path)
		if serr != nil {
			slog.Warn("auto-reindex: scan failed", "error", serr)
			continue
		}
		if sig == last {
			continue
		}
		slog.Info("auto-reindex: change detected, rebuilding database",
			"dirs", sig.dirCount, "prev_dirs", last.dirCount)
		if rerr := s.reindex(assembly64Path, dbPath); rerr != nil {
			slog.Error("auto-reindex: rebuild failed", "error", rerr)
			continue
		}
		last = sig
		slog.Info("auto-reindex: database hot-swapped", "dirs", sig.dirCount)
	}
}

// HandleMenu returns menu entries for the given path using SQL queries.
func (s *SQLiteServer) HandleMenu(path string) string {
	slog.Debug("HandleMenu called", "path", path)

	if path == "" {
		// Root menu - list categories
		return s.getRootMenu()
	}

	parts := strings.Split(path, "/")
	slog.Debug("HandleMenu parts", "parts", parts, "len", len(parts))

	if len(parts) == 0 {
		return "OK 0\n.\n"
	}

	// Letter-grid menu: when the user enters "Browse A-Z" we route here and
	// return 27 list entries (A..Z plus #) that the client renders as a grid.
	if parts[len(parts)-1] == "A-Z" {
		return s.getLetterGridMenu(path)
	}

	category := parts[0]

	switch category {
	case "Games":
		return s.getGamesMenu(parts)
	case "Demos":
		return s.getDemosMenu(parts)
	case "Music":
		return s.getMusicMenu(parts)
	case "Intros":
		return s.getIntrosMenu(parts)
	case "Graphics":
		return s.getGraphicsMenu(parts)
	case "Discmags":
		return s.getDiscmagsMenu(parts)
	case "MYFILES":
		return s.getMyFilesMenu(parts)
	default:
		return "OK 0\n.\n"
	}
}

func (s *SQLiteServer) getRootMenu() string {
	var b strings.Builder
	// Static root menu with short type codes: f=folder, b=browse, l=list
	categories := []string{"Games", "Demos", "Music", "Intros", "Graphics", "Discmags"}

	// +1 for My Files entry
	b.WriteString(fmt.Sprintf("OK %d\n", len(categories)+1))
	for _, cat := range categories {
		count, _ := s.DB().CountByPathPrefix(cat + "/")
		// Protocol format: type|name|path|count
		b.WriteString(fmt.Sprintf("f|%s|%s|%d\n", cat, cat, count))
	}
	// Add My Files as special folder pointing to MYFILES handler
	b.WriteString("f|My Files|MYFILES|0\n")
	b.WriteString(".\n")
	return b.String()
}

func (s *SQLiteServer) getGamesMenu(parts []string) string {
	slog.Debug("getGamesMenu called", "parts", parts, "len", len(parts))

	if len(parts) == 1 {
		// Games/ - list sources
		sources := []struct {
			name   string
			source string
		}{
			{"CSDB", "CSDB"},
			{"C64.com", "C64com"},
			{"Gamebase", "Gamebase"},
			{"Guybrush", "Guybrush"},
			{"OneLoad64", "OneLoad64"},
			{"Mayhem CRT", "Mayhem"},
			{"C64 Tapes", "C64Tapes"},
			{"Preservers", "Preservers"},
			{"SEUCK", "SEUCK"},
		}
		return s.formatSourceMenu("Games", sources)
	}

	source := parts[1]
	if len(parts) == 2 {
		slog.Debug("getGamesMenu: returning source menu", "source", source)
		// Games/Source/ - show Browse A-Z + special lists
		var entries []string

		// Count total entries for this source
		var totalCount int
		s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = 'Games' AND source = ?`, source).Scan(&totalCount)

		// Browse A-Z (type=b for browse)
		entries = append(entries, fmt.Sprintf("f|Browse A-Z|Games/%s/A-Z|%d", source, totalCount))

		// Add special lists for CSDB
		if source == "CSDB" {
			var top200Count int
			s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = 'Games' AND source = 'CSDB' AND top200_rank IS NOT NULL`).Scan(&top200Count)
			entries = append(entries, fmt.Sprintf("l|Top 200|Games/CSDB/Top200|%d", top200Count))

			// 4K count
			var fourKCount int
			s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = 'Games' AND source = 'CSDB' AND is_4k = 1`).Scan(&fourKCount)
			entries = append(entries, fmt.Sprintf("l|4K Competition|Games/CSDB/4k|%d", fourKCount))
		}

		var b strings.Builder
		b.WriteString(fmt.Sprintf("OK %d\n", len(entries)))
		for _, e := range entries {
			b.WriteString(e + "\n")
		}
		b.WriteString(".\n")
		return b.String()
	}

	// Games/Source/X - no longer needed, client uses LIST command
	return "OK 0\n.\n"
}

func (s *SQLiteServer) getDemosMenu(parts []string) string {
	if len(parts) == 1 {
		sources := []struct {
			name   string
			source string
		}{
			{"CSDB", "CSDB"},
			{"C64.com", "C64com"},
			{"Guybrush", "Guybrush"},
		}
		return s.formatSourceMenu("Demos", sources)
	}

	source := parts[1]
	if len(parts) == 2 {
		var entries []string

		// Count total entries for this source
		var totalCount int
		s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = 'Demos' AND source = ?`, source).Scan(&totalCount)

		// Browse A-Z (type=b for browse)
		entries = append(entries, fmt.Sprintf("f|Browse A-Z|Demos/%s/A-Z|%d", source, totalCount))

		if source == "CSDB" {
			// Top 200
			var top200Count int
			s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = 'Demos' AND source = 'CSDB' AND top200_rank IS NOT NULL`).Scan(&top200Count)
			entries = append(entries, fmt.Sprintf("l|Top 200|Demos/CSDB/Top200|%d", top200Count))

			// Top 500
			var top500Count int
			s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = 'Demos' AND source = 'CSDB' AND top500_rank IS NOT NULL`).Scan(&top500Count)
			entries = append(entries, fmt.Sprintf("l|Top 500|Demos/CSDB/Top500|%d", top500Count))

			// By Year
			var yearCount int
			s.DB().conn.QueryRow(`SELECT COUNT(DISTINCT year) FROM entries WHERE category = 'Demos' AND source = 'CSDB' AND year IS NOT NULL AND year != ''`).Scan(&yearCount)
			entries = append(entries, fmt.Sprintf("f|By Year|Demos/CSDB/Year|%d", yearCount))

			// Year Top 20
			var yearTop20Count int
			s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = 'Demos' AND source = 'CSDB' AND year_rank IS NOT NULL`).Scan(&yearTop20Count)
			entries = append(entries, fmt.Sprintf("f|Year Top 20|Demos/CSDB/Year-top20|%d", yearTop20Count))

			// Onefile
			var onefileCount int
			s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = 'Demos' AND source = 'CSDB' AND is_onefile = 1`).Scan(&onefileCount)
			entries = append(entries, fmt.Sprintf("f|Onefile|Demos/CSDB/Onefile|%d", onefileCount))
		}

		var b strings.Builder
		b.WriteString(fmt.Sprintf("OK %d\n", len(entries)))
		for _, e := range entries {
			b.WriteString(e + "\n")
		}
		b.WriteString(".\n")
		return b.String()
	}

	// Handle sub-paths
	if len(parts) >= 3 {
		subPath := parts[2]
		switch subPath {
		case "Year":
			if len(parts) == 3 {
				return s.getYearMenu("Demos", "CSDB")
			}
		case "Year-top20":
			if len(parts) == 3 {
				return s.getYearTop20Menu("Demos", "CSDB")
			}
		case "Onefile":
			if len(parts) == 3 {
				return s.getOnefileLetters()
			}
		}
	}

	return "OK 0\n.\n"
}

func (s *SQLiteServer) getMusicMenu(parts []string) string {
	if len(parts) == 1 {
		sources := []struct {
			name   string
			source string
		}{
			{"CSDB", "CSDB"},
			{"HVSC", "HVSC"},
			{"2SID Collection", "2sid"},
			{"3SID Collection", "3sid"},
		}
		return s.formatSourceMenu("Music", sources)
	}

	source := parts[1]
	if len(parts) == 2 {
		var entries []string

		// Count total entries for this source
		var totalCount int
		s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = 'Music' AND source = ?`, source).Scan(&totalCount)

		// Browse A-Z (type=b for browse)
		entries = append(entries, fmt.Sprintf("f|Browse A-Z|Music/%s/A-Z|%d", source, totalCount))

		if source == "CSDB" {
			var top200Count int
			s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = 'Music' AND source = 'CSDB' AND top200_rank IS NOT NULL`).Scan(&top200Count)
			entries = append(entries, fmt.Sprintf("l|Top 200|Music/CSDB/Top200|%d", top200Count))
		}

		var b strings.Builder
		b.WriteString(fmt.Sprintf("OK %d\n", len(entries)))
		for _, e := range entries {
			b.WriteString(e + "\n")
		}
		b.WriteString(".\n")
		return b.String()
	}

	return "OK 0\n.\n"
}

func (s *SQLiteServer) getIntrosMenu(parts []string) string {
	if len(parts) == 1 {
		sources := []struct {
			name   string
			source string
		}{
			{"CSDB", "CSDB"},
			{"C64.org", "C64org"},
		}
		return s.formatSourceMenu("Intros", sources)
	}

	source := parts[1]
	if len(parts) == 2 {
		var entries []string

		// Count total entries for this source
		var totalCount int
		s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = 'Intros' AND source = ?`, source).Scan(&totalCount)

		// Browse A-Z (type=b for browse)
		entries = append(entries, fmt.Sprintf("f|Browse A-Z|Intros/%s/A-Z|%d", source, totalCount))

		var b strings.Builder
		b.WriteString(fmt.Sprintf("OK %d\n", len(entries)))
		for _, e := range entries {
			b.WriteString(e + "\n")
		}
		b.WriteString(".\n")
		return b.String()
	}

	return "OK 0\n.\n"
}

func (s *SQLiteServer) getGraphicsMenu(parts []string) string {
	if len(parts) == 1 {
		// Graphics has only one source, show Browse A-Z directly
		var totalCount int
		s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = 'Graphics' AND source = 'CSDB'`).Scan(&totalCount)

		var b strings.Builder
		b.WriteString("OK 1\n")
		b.WriteString(fmt.Sprintf("f|Browse A-Z|Graphics/A-Z|%d\n", totalCount))
		b.WriteString(".\n")
		return b.String()
	}

	return "OK 0\n.\n"
}

func (s *SQLiteServer) getDiscmagsMenu(parts []string) string {
	if len(parts) == 1 {
		// Discmags has only one source, show Browse A-Z directly
		var totalCount int
		s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = 'Discmags' AND source = 'CSDB'`).Scan(&totalCount)

		var b strings.Builder
		b.WriteString("OK 1\n")
		b.WriteString(fmt.Sprintf("f|Browse A-Z|Discmags/A-Z|%d\n", totalCount))
		b.WriteString(".\n")
		return b.String()
	}

	return "OK 0\n.\n"
}

type letterCount struct {
	letter string
	count  int
}

func (s *SQLiteServer) getLetterCounts(category, source string) []letterCount {
	rows, err := s.DB().conn.Query(`
		SELECT
			CASE
				WHEN SUBSTR(UPPER(normalized_title), 1, 1) BETWEEN 'A' AND 'Z'
				THEN SUBSTR(UPPER(normalized_title), 1, 1)
				ELSE '#'
			END as letter,
			COUNT(*) as cnt
		FROM entries
		WHERE category = ? AND source = ?
		GROUP BY letter
		ORDER BY CASE WHEN letter = '#' THEN 0 ELSE 1 END, letter`, category, source)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []letterCount
	for rows.Next() {
		var l letterCount
		if err := rows.Scan(&l.letter, &l.count); err == nil {
			result = append(result, l)
		}
	}
	return result
}

// getLetterGridMenu returns a menu of 27 entries (A..Z plus '#') for the
// given A-Z path. Each entry is type 'l' so the client enters it as a LIST
// filtered by first letter. The full path for each entry is the input path
// plus '/' plus the letter, e.g. "Games/CSDB/A-Z/A". Counts come from
// getLetterCounts; letters absent from the DB get count=0 but still appear
// so the grid layout is predictable and the user sees all 27 positions.
func (s *SQLiteServer) getLetterGridMenu(path string) string {
	// The parent of /A-Z is Category[/Source] — strip the trailing /A-Z to
	// reuse the existing category+source resolution in getLetterCounts.
	parent := strings.TrimSuffix(path, "/A-Z")

	parts := strings.Split(parent, "/")
	category := ""
	source := ""
	if len(parts) >= 1 {
		category = parts[0]
	}
	switch category {
	case "Games", "Demos", "Music", "Intros":
		if len(parts) >= 2 {
			source = parts[1]
		} else {
			return "OK 0\n.\n"
		}
	case "Graphics", "Discmags":
		source = "CSDB"
	default:
		return "OK 0\n.\n"
	}

	// Build a map letter -> count to merge with the fixed A..Z,# layout.
	counts := map[string]int{}
	for _, lc := range s.getLetterCounts(category, source) {
		counts[lc.letter] = lc.count
	}

	letters := []string{
		"A", "B", "C", "D", "E", "F", "G", "H", "I",
		"J", "K", "L", "M", "N", "O", "P", "Q", "R",
		"S", "T", "U", "V", "W", "X", "Y", "Z", "#",
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("OK %d\n", len(letters)))
	for _, l := range letters {
		// Protocol: type|name|path|count — name is just the letter itself
		// so the client can render it in a grid cell.
		b.WriteString(fmt.Sprintf("l|%s|%s/%s|%d\n", l, path, l, counts[l]))
	}
	b.WriteString(".\n")
	return b.String()
}

// HandleLetters returns letter counts for a given path.
// Used by client to display letter picker UI.
// Response format: OK <count>\n then <letter>|<count>\n per letter, then .\n
func (s *SQLiteServer) HandleLetters(path string) string {
	slog.Debug("HandleLetters called", "path", path)

	// Parse path to extract category and source
	// Examples: "Games/CSDB", "Demos/C64com", "Graphics", "Discmags"
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return "OK 0\n.\n"
	}

	category := parts[0]
	source := ""

	// Determine source based on category
	switch category {
	case "Games", "Demos", "Music", "Intros":
		if len(parts) >= 2 {
			source = parts[1]
		} else {
			return "OK 0\n.\n"
		}
	case "Graphics", "Discmags":
		// Single source categories
		source = "CSDB"
	default:
		return "OK 0\n.\n"
	}

	letters := s.getLetterCounts(category, source)
	if len(letters) == 0 {
		return "OK 0\n.\n"
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("OK %d\n", len(letters)))
	for _, l := range letters {
		b.WriteString(fmt.Sprintf("%s|%d\n", l.letter, l.count))
	}
	b.WriteString(".\n")
	return b.String()
}

func (s *SQLiteServer) formatSourceMenu(category string, sources []struct {
	name   string
	source string
}) string {
	var entries []string
	for _, src := range sources {
		var count int
		s.DB().conn.QueryRow(`SELECT COUNT(*) FROM entries WHERE category = ? AND source = ?`, category, src.source).Scan(&count)
		if count > 0 {
			// Type 'f' for folder (navigates to source submenu)
			entries = append(entries, fmt.Sprintf("f|%s|%s/%s|%d", src.name, category, src.source, count))
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("OK %d\n", len(entries)))
	for _, e := range entries {
		b.WriteString(e + "\n")
	}
	b.WriteString(".\n")
	return b.String()
}

func (s *SQLiteServer) getYearMenu(category, source string) string {
	rows, err := s.DB().conn.Query(`
		SELECT year, COUNT(*) as cnt
		FROM entries
		WHERE category = ? AND source = ? AND year IS NOT NULL AND year != ''
		GROUP BY year
		ORDER BY year DESC`, category, source)
	if err != nil {
		return "OK 0\n.\n"
	}
	defer rows.Close()

	var entries []string
	for rows.Next() {
		var year string
		var count int
		if err := rows.Scan(&year, &count); err == nil {
			entries = append(entries, fmt.Sprintf("l|%s|%s/%s/Year/%s|%d", year, category, source, year, count))
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("OK %d\n", len(entries)))
	for _, e := range entries {
		b.WriteString(e + "\n")
	}
	b.WriteString(".\n")
	return b.String()
}

func (s *SQLiteServer) getYearTop20Menu(category, source string) string {
	rows, err := s.DB().conn.Query(`
		SELECT year, COUNT(*) as cnt
		FROM entries
		WHERE category = ? AND source = ? AND year_rank IS NOT NULL
		GROUP BY year
		ORDER BY year DESC`, category, source)
	if err != nil {
		return "OK 0\n.\n"
	}
	defer rows.Close()

	var entries []string
	for rows.Next() {
		var year string
		var count int
		if err := rows.Scan(&year, &count); err == nil {
			entries = append(entries, fmt.Sprintf("l|%s|%s/%s/Year-top20/%s|%d", year, category, source, year, count))
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("OK %d\n", len(entries)))
	for _, e := range entries {
		b.WriteString(e + "\n")
	}
	b.WriteString(".\n")
	return b.String()
}

func (s *SQLiteServer) getOnefileLetters() string {
	rows, err := s.DB().conn.Query(`
		SELECT
			CASE
				WHEN SUBSTR(UPPER(normalized_title), 1, 1) BETWEEN 'A' AND 'Z'
				THEN SUBSTR(UPPER(normalized_title), 1, 1)
				ELSE '#'
			END as letter,
			COUNT(*) as cnt
		FROM entries
		WHERE category = 'Demos' AND source = 'CSDB' AND is_onefile = 1
		GROUP BY letter
		ORDER BY CASE WHEN letter = '#' THEN 0 ELSE 1 END, letter`)
	if err != nil {
		return "OK 0\n.\n"
	}
	defer rows.Close()

	var entries []string
	for rows.Next() {
		var letter string
		var count int
		if err := rows.Scan(&letter, &count); err == nil {
			entries = append(entries, fmt.Sprintf("l|%s|Demos/CSDB/Onefile/%s|%d", letter, letter, count))
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("OK %d\n", len(entries)))
	for _, e := range entries {
		b.WriteString(e + "\n")
	}
	b.WriteString(".\n")
	return b.String()
}

// HandleListPath returns entries matching a path using SQL, grouped by title.
// This matches the JSON server behavior - entries with same normalized title are
// grouped together with a count, allowing the user to select and see all releases.
// The letter parameter is optional - if provided, it filters by first letter of title.
func (s *SQLiteServer) HandleListPath(path string, offset, count int, letter string) string {
	// Parse path to determine category, source, and filters
	parts := strings.Split(path, "/")
	if len(parts) < 1 {
		return "OK 0 0\n.\n"
	}

	category := parts[0]
	var source string
	var letterFilter string

	// Use provided letter parameter if given
	if letter != "" {
		letterFilter = strings.ToUpper(letter)
	}

	// Graphics and Discmags have only one source (CSDB)
	if category == "Graphics" || category == "Discmags" {
		source = "CSDB"
	} else if len(parts) >= 2 {
		source = parts[1]
	} else {
		return "OK 0 0\n.\n"
	}

	// Build query based on path
	var whereClause string
	var args []interface{}

	whereClause = "category = ? AND source = ?"
	args = append(args, category, source)

	// Check for special paths
	if len(parts) >= 3 {
		subPath := parts[2]
		switch subPath {
		case "A-Z":
			// Letter-grid leaf path: Category/Source/A-Z/<letter>.
			// The /A-Z/ level exists purely so BROWSE A-Z behaves as a folder
			// that opens the letter grid; the letter filter lives one level
			// deeper.
			if len(parts) >= 4 {
				letterFilter = parts[3]
			}
		case "Top200":
			whereClause += " AND top200_rank IS NOT NULL"
		case "Top500":
			whereClause += " AND top500_rank IS NOT NULL"
		case "4k":
			whereClause += " AND is_4k = 1"
		case "Onefile":
			whereClause += " AND is_onefile = 1"
			if len(parts) >= 4 {
				letterFilter = parts[3]
			}
		case "Year":
			if len(parts) >= 4 {
				whereClause += " AND year = ?"
				args = append(args, parts[3])
			}
		case "Year-top20":
			whereClause += " AND year_rank IS NOT NULL"
			if len(parts) >= 4 {
				whereClause += " AND year = ?"
				args = append(args, parts[3])
			}
		default:
			// It's a letter at the Category/Source/<letter> level (legacy
			// path without intermediate A-Z). Keep supporting it so existing
			// clients don't break.
			if len(subPath) == 1 && ((subPath >= "A" && subPath <= "Z") || subPath == "#") {
				letterFilter = subPath
			}
		}
	}

	// Apply letter filter
	if letterFilter != "" {
		if letterFilter == "#" {
			whereClause += " AND (normalized_title < 'a' OR normalized_title >= '{')"
		} else {
			letter := strings.ToLower(letterFilter)
			whereClause += " AND normalized_title >= ? AND normalized_title < ?"
			args = append(args, letter, string(letter[0]+1))
		}
	}

	// Always group by title - this is the expected behavior
	return s.handleGroupedList(whereClause, args, offset, count)
}

func (s *SQLiteServer) handleGroupedList(whereClause string, args []interface{}, offset, count int) string {
	// Get total count of distinct titles
	countQuery := fmt.Sprintf(`SELECT COUNT(DISTINCT normalized_title) FROM entries WHERE %s`, whereClause)
	var total int
	s.DB().conn.QueryRow(countQuery, args...).Scan(&total)

	if offset >= total {
		return fmt.Sprintf("OK 0 %d\n.\n", total)
	}

	// Get grouped entries with first entry ID, max trainers, and category
	query := fmt.Sprintf(`
		SELECT MIN(id), title, category, COUNT(*) as release_count, MAX(COALESCE(trainers, -1)) as max_trainers
		FROM entries
		WHERE %s
		GROUP BY normalized_title
		ORDER BY normalized_title
		LIMIT ? OFFSET ?`, whereClause)

	args = append(args, count, offset)
	rows, err := s.DB().conn.Query(query, args...)
	if err != nil {
		return fmt.Sprintf("OK 0 %d\n.\n", total)
	}
	defer rows.Close()

	var results []string
	for rows.Next() {
		var id int64
		var title, category string
		var releaseCount, maxTrainers int
		if err := rows.Scan(&id, &title, &category, &releaseCount, &maxTrainers); err == nil {
			// Format: ID|Name|Category|Count|Trainers (same as JSON server)
			results = append(results, fmt.Sprintf("%d|%s|%s|%d|%d", id, title, category, releaseCount, maxTrainers))
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("OK %d %d\n", len(results), total))
	for _, r := range results {
		b.WriteString(r + "\n")
	}
	b.WriteString(".\n")
	return b.String()
}

// HandleSearch performs full-text search using FTS5.
func (s *SQLiteServer) HandleSearch(query, category string, offset, count int) string {
	// Escape special FTS characters
	ftsQuery := strings.ReplaceAll(query, "\"", "\"\"")
	ftsQuery = "\"" + ftsQuery + "\"*"

	var whereClause string
	var args []interface{}

	whereClause = "entries_fts MATCH ?"
	args = append(args, ftsQuery)

	if category != "" && !strings.EqualFold(category, "All") {
		whereClause += " AND e.category = ?"
		args = append(args, category)
	}

	// Get total count
	countQuery := fmt.Sprintf(`
		SELECT COUNT(*) FROM entries e
		JOIN entries_fts ON e.id = entries_fts.rowid
		WHERE %s`, whereClause)
	var total int
	s.DB().conn.QueryRow(countQuery, args...).Scan(&total)

	if offset >= total {
		return fmt.Sprintf("OK 0 %d\n.\n", total)
	}

	// Get results
	selectQuery := fmt.Sprintf(`
		SELECT e.id, e.title, e.category, COALESCE(e.trainers, -1) as trainers
		FROM entries e
		JOIN entries_fts ON e.id = entries_fts.rowid
		WHERE %s
		ORDER BY rank
		LIMIT ? OFFSET ?`, whereClause)

	args = append(args, count, offset)
	rows, err := s.DB().conn.Query(selectQuery, args...)
	if err != nil {
		return fmt.Sprintf("OK 0 %d\n.\n", total)
	}
	defer rows.Close()

	var results []string
	for rows.Next() {
		var id int64
		var title, cat string
		var trainers int
		if err := rows.Scan(&id, &title, &cat, &trainers); err == nil {
			// Format: ID|Name|Category|Count|Trainers (Count=1 for search results)
			results = append(results, fmt.Sprintf("%d|%s|%s|1|%d", id, title, cat, trainers))
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("OK %d %d\n", len(results), total))
	for _, r := range results {
		b.WriteString(r + "\n")
	}
	b.WriteString(".\n")
	return b.String()
}

// HandleInfo returns detailed information about an entry.
// Format: OK\n followed by LABEL|value lines, ending with .\n
func (s *SQLiteServer) HandleInfo(id int64) string {
	entry, err := s.DB().GetEntryByID(id)
	if err != nil || entry == nil {
		return "ERR Entry not found\n"
	}

	var b strings.Builder
	b.WriteString("OK\n")
	b.WriteString(fmt.Sprintf("NAME|%s\n", entry.Title))
	b.WriteString(fmt.Sprintf("GROUP|%s\n", entry.GroupName))
	if entry.Year != "" {
		b.WriteString(fmt.Sprintf("YEAR|%s\n", entry.Year))
	}
	b.WriteString(fmt.Sprintf("TYPE|%s\n", entry.FileType))
	if entry.ReleaseName != "" {
		b.WriteString(fmt.Sprintf("REL|%s\n", entry.ReleaseName))
	}
	if entry.Author != "" {
		b.WriteString(fmt.Sprintf("AUTHOR|%s\n", entry.Author))
	}
	if entry.Top200Rank != nil {
		b.WriteString(fmt.Sprintf("TOP200|#%d\n", *entry.Top200Rank))
	}
	if entry.Rating != nil && *entry.Rating > 0 {
		b.WriteString(fmt.Sprintf("RATING|%.1f\n", *entry.Rating))
	}
	b.WriteString(".\n")
	return b.String()
}

// HandleReleases returns all releases with the same normalized title.
func (s *SQLiteServer) HandleReleases(path, title string, offset, count int) string {
	normalizedTitle := normalizeTitle(title)

	// Parse path for category/source filtering
	parts := strings.Split(path, "/")
	var category, source string
	if len(parts) >= 1 {
		category = parts[0]
	}

	// Graphics and Discmags have only one source (CSDB), so path is Category/Letter
	// Other categories have Category/Source/Letter format
	if category == "Graphics" || category == "Discmags" {
		source = "CSDB"
	} else if len(parts) >= 2 {
		// Check if parts[1] is a letter (single char) or a source name
		if len(parts[1]) == 1 && ((parts[1] >= "A" && parts[1] <= "Z") || parts[1] == "#") {
			// It's a letter, not a source - don't filter by source
			source = ""
		} else {
			source = parts[1]
		}
	}

	// Build query
	whereClause := "normalized_title = ?"
	args := []interface{}{normalizedTitle}

	if category != "" {
		whereClause += " AND category = ?"
		args = append(args, category)
	}
	if source != "" {
		whereClause += " AND source = ?"
		args = append(args, source)
	}

	// Get total count
	var total int
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM entries WHERE %s`, whereClause)
	s.DB().conn.QueryRow(countQuery, args...).Scan(&total)

	if offset >= total {
		return fmt.Sprintf("OK 0 %d\n.\n", total)
	}

	// Get releases
	query := fmt.Sprintf(`
		SELECT id, group_name, year, file_type, COALESCE(trainers, -1) as trainers
		FROM entries
		WHERE %s
		ORDER BY group_name, year
		LIMIT ? OFFSET ?`, whereClause)

	args = append(args, count, offset)
	rows, err := s.DB().conn.Query(query, args...)
	if err != nil {
		return fmt.Sprintf("OK 0 %d\n.\n", total)
	}
	defer rows.Close()

	var results []string
	for rows.Next() {
		var id int64
		var group, year, fileType string
		var trainers int
		if err := rows.Scan(&id, &group, &year, &fileType, &trainers); err == nil {
			// Format: ID|Group|Year|Type|Trainers (name omitted since all same title)
			results = append(results, fmt.Sprintf("%d|%s|%s|%s|%d", id, group, year, fileType, trainers))
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("OK %d %d\n", len(results), total))
	for _, r := range results {
		b.WriteString(r + "\n")
	}
	b.WriteString(".\n")
	return b.String()
}

// GetFilePath returns the full path to the primary file of an entry.
func (s *SQLiteServer) GetFilePath(id int64) (string, error) {
	entry, err := s.DB().GetEntryByID(id)
	if err != nil || entry == nil {
		return "", fmt.Errorf("entry not found")
	}

	return filepath.Join(s.assembly64Path, entry.Path, entry.PrimaryFile), nil
}

// getMyFilesMenu returns directory listing for the MYFILES path.
// MYFILES paths map to ./myfiles/ on the filesystem.
func (s *SQLiteServer) getMyFilesMenu(parts []string) string {
	// Build path: ./myfiles/ + subdirectory
	subPath := ""
	if len(parts) > 1 {
		subPath = strings.Join(parts[1:], "/")
	}
	dirPath := filepath.Join("./myfiles", subPath)

	// Security: Prevent path traversal attacks by validating resolved path
	absBase, err := filepath.Abs("./myfiles")
	if err != nil {
		slog.Error("getMyFilesMenu: failed to resolve base path", "error", err)
		return "OK 0\n.\n"
	}
	absFull, err := filepath.Abs(dirPath)
	if err != nil {
		slog.Error("getMyFilesMenu: failed to resolve full path", "path", dirPath, "error", err)
		return "OK 0\n.\n"
	}
	if !strings.HasPrefix(absFull, absBase) {
		slog.Warn("getMyFilesMenu: path traversal attempt blocked", "requested", subPath, "resolved", absFull)
		return "OK 0\n.\n"
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		slog.Error("getMyFilesMenu: failed to read directory", "path", dirPath, "error", err)
		return "OK 0\n.\n"
	}

	var b strings.Builder
	var validEntries []string

	for _, entry := range entries {
		name := entry.Name()

		// Skip hidden files and directories
		if strings.HasPrefix(name, ".") {
			continue
		}

		entryPath := "MYFILES"
		if subPath != "" {
			entryPath = "MYFILES/" + subPath + "/" + name
		} else {
			entryPath = "MYFILES/" + name
		}

		if entry.IsDir() {
			validEntries = append(validEntries, fmt.Sprintf("D|%s|%s|0", name, entryPath))
		} else {
			ext := strings.ToLower(filepath.Ext(name))
			if isValidMyFilesExtension(ext) {
				validEntries = append(validEntries, fmt.Sprintf("F|%s|%s|0", name, entryPath))
			}
		}
	}

	// Sort entries alphabetically (case-insensitive)
	sort.Slice(validEntries, func(i, j int) bool {
		return strings.ToLower(validEntries[i]) < strings.ToLower(validEntries[j])
	})

	b.WriteString(fmt.Sprintf("OK %d\n", len(validEntries)))
	for _, entry := range validEntries {
		b.WriteString(entry + "\n")
	}
	b.WriteString(".\n")
	return b.String()
}

// isValidMyFilesExtension checks if a file extension is valid for MYFILES listing.
// ext is expected to be lowercase (callers pass strings.ToLower(filepath.Ext(...))).
func isValidMyFilesExtension(ext string) bool {
	validExts := map[string]bool{
		".prg": true, ".d64": true, ".t64": true, ".crt": true,
		".sid": true, ".tap": true, ".d71": true, ".d81": true, ".g64": true,
		".ue2": true,
	}
	return validExts[ext]
}

// isUploadOnlyExtension reports whether a file is transferred to the device but
// never launched. Ultimate firmware/flash-patch files (.ue2) fall here: there is
// no REST flash endpoint, so we only FTP them across and the user applies the
// patch from the Ultimate's on-device menu. ext is expected to be lowercase.
func isUploadOnlyExtension(ext string) bool {
	return ext == ".ue2"
}
