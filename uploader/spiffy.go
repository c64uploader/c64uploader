// Spiffy-compatible HTTP server.
//
// Speaks the same /leet/search/ HTTP API as Spiffy_Home_Assembly_64 so the
// Ultimate II+ / C64 Ultimate firmware's built-in Assembly64 search browser
// can be pointed at our local mirror instead of assembly64.com.
//
// See SPIFFY_HTTP_API.md for the wire format. This file is a clean-room
// implementation against that spec — no code is borrowed from Spiffy
// (GPLv3) or the Ultimate firmware (GPLv3).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Boundary for the multipart/mixed binary download response. Any
// RFC-2046-legal token works — the firmware reads the boundary from the
// Content-Type header.
const spiffyMultipartBoundary = "c64uploaderboundary"

// Maximum result-set size; a single search query won't return more than
// this many entries even if the firmware asks for more.
const spiffyMaxCount = 1000

// Default count when the firmware uses the no-pagination URL form.
const spiffyDefaultCount = 100

// Mapping from our SQLite entries.category strings to the small integer
// codes the firmware echoes in URLs. The firmware doesn't decode them;
// any consistent mapping works. See SPIFFY_HTTP_API.md "Category codes".
var spiffyCategoryCodes = map[string]int{
	"Games":    1,
	"Demos":    2,
	"Music":    3,
	"Intros":   4,
	"Graphics": 5,
	"Discmags": 6,
}

func spiffyCategoryCode(category string) int {
	if c, ok := spiffyCategoryCodes[category]; ok {
		return c
	}
	return 0
}

func spiffyCategoryFromCode(code int) string {
	for name, c := range spiffyCategoryCodes {
		if c == code {
			return name
		}
	}
	return ""
}

// SpiffyServer wraps the SQLite-backed search and serves the Spiffy API
// (and the optional web UI under the same listener — see web.go).
type SpiffyServer struct {
	sqliteServer   *SQLiteServer // SQLite backend; read the live DB via sqliteServer.DB()
	apiClient      *APIClient    // for the web POST /api/run/<id>
	assembly64Path string
	clientID       string // optional gate; empty = no gate
}

// startSpiffyHTTP starts the HTTP listener on the given port. It serves
//   - /leet/search/*  : Spiffy-compatible API for the Ultimate firmware
//     built-in Assembly64 browser
//   - /api/* and /    : optional mobile-friendly web UI (see web.go)
//
// Both paths share the same listener and the same SQLite backend.
// Intended to be run in a goroutine alongside the TCP line-protocol server.
func startSpiffyHTTP(port int, sqliteServer *SQLiteServer, apiClient *APIClient, assembly64Path, clientID string) error {
	srv := &SpiffyServer{
		sqliteServer:   sqliteServer,
		apiClient:      apiClient,
		assembly64Path: assembly64Path,
		clientID:       clientID,
	}
	mux := http.NewServeMux()

	// Spiffy-compatible firmware-browser API.
	mux.HandleFunc("/leet/search/aql/presets", srv.handlePresets)
	mux.HandleFunc("/leet/search/aql/", srv.handleSearchPaginated)
	mux.HandleFunc("/leet/search/aql", srv.handleSearch)
	mux.HandleFunc("/leet/search/entries/", srv.handleEntries)
	mux.HandleFunc("/leet/search/bin/", srv.handleBinary)

	// Mobile-friendly web UI (in web.go).
	srv.registerWebRoutes(mux)

	addr := fmt.Sprintf(":%d", port)
	slog.Info("HTTP server listening", "addr", addr)
	fmt.Printf("HTTP server listening on :%d (Spiffy /leet/search/, web UI /)\n", port)
	return http.ListenAndServe(addr, srv.cors(mux))
}

// cors wraps a handler with the CORS + Client-Id-gate middleware.
func (s *SpiffyServer) cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Client-Id, Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if s.clientID != "" {
			if r.Header.Get("Client-Id") != s.clientID {
				slog.Debug("spiffy: rejected request with bad/missing Client-Id", "path", r.URL.Path)
				http.Error(w, "client-id required", http.StatusUnauthorized)
				return
			}
		}

		slog.Debug("spiffy", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery)
		h.ServeHTTP(w, r)
	})
}

// -----------------------------------------------------------------------------
// /leet/search/aql/presets — dropdown choices for the firmware search form.
// -----------------------------------------------------------------------------

type presetField struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

func (s *SpiffyServer) handlePresets(w http.ResponseWriter, r *http.Request) {
	// "category" comes from spiffyCategoryCodes keys (in fixed order).
	// "type" lists the file extensions we recognise.
	// "sort" / "order" let the user pick result ordering.
	presets := []presetField{
		{Type: "category", Values: []string{"Games", "Demos", "Music", "Intros", "Graphics", "Discmags"}},
		{Type: "type", Values: []string{"d64", "prg", "crt", "sid", "tap", "g64", "d71", "d81", "t64", "bin"}},
		{Type: "sort", Values: []string{"name", "date", "year"}},
		{Type: "order", Values: []string{"asc", "desc"}},
	}
	writeJSON(w, presets)
}

// -----------------------------------------------------------------------------
// /leet/search/aql{,/<start>/<count>}?query=<aql> — search.
// -----------------------------------------------------------------------------

type searchResult struct {
	Name         string `json:"name"`
	ID           string `json:"id"`
	Category     int    `json:"category"`
	SiteCategory int    `json:"siteCategory"`
	SiteRating   int    `json:"siteRating"`
	Group        string `json:"group,omitempty"`
	Handle       string `json:"handle,omitempty"`
	Year         int    `json:"year"`
	Rating       int    `json:"rating"`
	Updated      string `json:"updated"`
	Released     string `json:"released,omitempty"`
}

func (s *SpiffyServer) handleSearch(w http.ResponseWriter, r *http.Request) {
	s.runSearch(w, r, 0, spiffyDefaultCount)
}

// /leet/search/aql/<start>/<count>?query=...
var searchPaginatedRE = regexp.MustCompile(`^/leet/search/aql/(\d+)/(\d+)/?$`)

func (s *SpiffyServer) handleSearchPaginated(w http.ResponseWriter, r *http.Request) {
	m := searchPaginatedRE.FindStringSubmatch(r.URL.Path)
	if m == nil {
		// Could be /leet/search/aql/presets — already routed by mux.
		// Anything else under /aql/ that didn't match is a 404.
		http.NotFound(w, r)
		return
	}
	start, _ := strconv.Atoi(m[1])
	count, _ := strconv.Atoi(m[2])
	if count <= 0 {
		count = spiffyDefaultCount
	}
	if count > spiffyMaxCount {
		count = spiffyMaxCount
	}
	if start < 0 {
		start = 0
	}
	s.runSearch(w, r, start, count)
}

func (s *SpiffyServer) runSearch(w http.ResponseWriter, r *http.Request, start, count int) {
	aql := r.URL.Query().Get("query")
	clauses, _ := parseAQL(aql)
	slog.Debug("spiffy search", "aql", aql, "clauses", clauses, "start", start, "count", count)

	entries, err := s.searchDB(clauses, start, count)
	if err != nil {
		slog.Error("spiffy search failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	results := make([]searchResult, 0, len(entries))
	for _, e := range entries {
		results = append(results, entryToSearchResult(e))
	}
	writeJSON(w, results)
}

func entryToSearchResult(e *Entry) searchResult {
	rating := 0
	if e.Rating != nil {
		// Rating in DB is 0.0-10.0; expose as integer 0-10.
		rating = int(*e.Rating + 0.5)
	}
	year := 0
	if y, err := strconv.Atoi(e.Year); err == nil {
		year = y
	}
	return searchResult{
		Name:         e.Title,
		ID:           strconv.FormatInt(e.ID, 10),
		Category:     spiffyCategoryCode(e.Category),
		SiteCategory: 0,
		SiteRating:   0,
		Group:        e.GroupName,
		Year:         year,
		Rating:       rating,
		Updated:      "", // we don't track an update timestamp; left blank
		Released:     "",
	}
}

// -----------------------------------------------------------------------------
// /leet/search/entries/<id>/<cat> — file list for an entry.
// -----------------------------------------------------------------------------

type entryFile struct {
	Path string `json:"path"`
	ID   int    `json:"id"`
	Size int64  `json:"size"`
}

type entryFiles struct {
	ContentEntry      []entryFile `json:"contentEntry"`
	IsContentByItself bool        `json:"isContentByItself"`
}

var entriesPathRE = regexp.MustCompile(`^/leet/search/entries/([^/]+)/([0-9]+)/?$`)

func (s *SpiffyServer) handleEntries(w http.ResponseWriter, r *http.Request) {
	m := entriesPathRE.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	// m[2] is category code; we don't actually need it (id is unique already)
	// but accept it for protocol compliance.
	_ = m[2]

	entry, err := s.sqliteServer.DB().GetEntryByID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if entry == nil {
		http.NotFound(w, r)
		return
	}

	files, err := s.filesForEntry(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// If the DB has no file rows for this entry, fall back to listing the
	// entry's directory on disk so the firmware always sees something to
	// drill into.
	if len(files) == 0 {
		files = s.scanEntryDir(entry)
	}

	resp := entryFiles{
		ContentEntry:      files,
		IsContentByItself: false,
	}
	writeJSON(w, resp)
}

func (s *SpiffyServer) filesForEntry(entryID int64) ([]entryFile, error) {
	rows, err := s.sqliteServer.DB().conn.Query(
		`SELECT name, size FROM files WHERE entry_id = ? ORDER BY name`,
		entryID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []entryFile{}
	idx := 0
	for rows.Next() {
		var name string
		var size int64
		if err := rows.Scan(&name, &size); err != nil {
			return nil, err
		}
		out = append(out, entryFile{Path: name, ID: idx, Size: size})
		idx++
	}
	return out, rows.Err()
}

func (s *SpiffyServer) scanEntryDir(entry *Entry) []entryFile {
	dir := filepath.Join(s.assembly64Path, entry.Path)
	infos, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := []entryFile{}
	idx := 0
	for _, fi := range infos {
		if fi.IsDir() {
			continue
		}
		st, err := fi.Info()
		if err != nil {
			continue
		}
		out = append(out, entryFile{Path: fi.Name(), ID: idx, Size: st.Size()})
		idx++
	}
	return out
}

// -----------------------------------------------------------------------------
// /leet/search/bin/<id>/<cat>/<idx>{/<filename>} — binary download.
// -----------------------------------------------------------------------------

// The firmware uses two URL shapes for binary download:
//
//	/leet/search/bin/<id>/<cat>/<idx>           (numeric index — from search results)
//	/leet/search/bin/<id>/<cat>/<filename>      (from filesystem_a64 file_open)
//
// We accept both. An optional 5th path component is tolerated but ignored
// (some implementations append the filename after the idx for human-
// readable URLs).
var binPathRE = regexp.MustCompile(`^/leet/search/bin/([^/]+)/([0-9]+)/([^/]+)(?:/[^/]+)?/?$`)

func (s *SpiffyServer) handleBinary(w http.ResponseWriter, r *http.Request) {
	m := binPathRE.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	thirdSeg, err := url.PathUnescape(m[3])
	if err != nil {
		http.Error(w, "bad path component", http.StatusBadRequest)
		return
	}

	entry, err := s.sqliteServer.DB().GetEntryByID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if entry == nil {
		http.NotFound(w, r)
		return
	}

	files, err := s.filesForEntry(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(files) == 0 {
		files = s.scanEntryDir(entry)
	}

	// Resolve thirdSeg to a file index. Integer → direct index; otherwise
	// look up by basename (case-insensitive), since the firmware sends the
	// filename verbatim from the entries response when going through
	// file_open.
	idx := -1
	if i, err := strconv.Atoi(thirdSeg); err == nil {
		if i >= 0 && i < len(files) {
			idx = i
		}
	}
	if idx < 0 {
		for i, f := range files {
			if strings.EqualFold(f.Path, thirdSeg) {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		http.NotFound(w, r)
		return
	}

	path := filepath.Join(s.assembly64Path, entry.Path, files[idx].Path)
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()

	// multipart/mixed body — single part.
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+spiffyMultipartBoundary)
	// http.ResponseWriter chunks automatically for large responses; let it.
	w.WriteHeader(http.StatusOK)

	bw := w.(io.Writer)

	fmt.Fprintf(bw, "--%s\r\n", spiffyMultipartBoundary)
	fmt.Fprintf(bw, "Content-Disposition: attachment; filename=%q\r\n", files[idx].Path)
	fmt.Fprintf(bw, "Content-Type: application/octet-stream\r\n")
	fmt.Fprintf(bw, "\r\n")
	if _, err := io.Copy(bw, f); err != nil {
		slog.Warn("spiffy binary stream interrupted", "err", err)
		return
	}
	fmt.Fprintf(bw, "\r\n--%s--\r\n", spiffyMultipartBoundary)
}

// -----------------------------------------------------------------------------
// AQL parser.
// -----------------------------------------------------------------------------

// AQLClause is one (key:value) pair from the firmware's query string.
// Op is "" for plain match, ">=" or ">" for the rating-style numeric
// comparator the firmware prepends.
type AQLClause struct {
	Key   string
	Op    string
	Value string
}

// Loose extractor — matches what the firmware emits and is forgiving
// about extra whitespace and missing parens.
var aqlClauseRE = regexp.MustCompile(`\(?(\w+):"?([^")]+)"?\)?`)

func parseAQL(query string) ([]AQLClause, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	matches := aqlClauseRE.FindAllStringSubmatch(query, -1)
	clauses := make([]AQLClause, 0, len(matches))
	for _, m := range matches {
		c := AQLClause{
			Key:   strings.ToLower(m[1]),
			Value: strings.TrimSpace(m[2]),
		}
		// Strip and remember a leading `>=` or `>` so e.g. (rating:>=8) is
		// distinguishable from (rating:8).
		switch {
		case strings.HasPrefix(c.Value, ">="):
			c.Op = ">="
			c.Value = strings.TrimSpace(c.Value[2:])
		case strings.HasPrefix(c.Value, ">"):
			c.Op = ">"
			c.Value = strings.TrimSpace(c.Value[1:])
		case strings.HasPrefix(c.Value, "<="):
			c.Op = "<="
			c.Value = strings.TrimSpace(c.Value[2:])
		case strings.HasPrefix(c.Value, "<"):
			c.Op = "<"
			c.Value = strings.TrimSpace(c.Value[1:])
		}
		clauses = append(clauses, c)
	}
	return clauses, nil
}

// -----------------------------------------------------------------------------
// SQL builder.
// -----------------------------------------------------------------------------

// searchDB translates AQL clauses into a parameterised SELECT against the
// entries table.
func (s *SpiffyServer) searchDB(clauses []AQLClause, start, count int) ([]*Entry, error) {
	where := []string{}
	args := []interface{}{}

	sortKey := "title"
	sortDir := "ASC"

	for _, c := range clauses {
		switch c.Key {
		case "name":
			where = append(where, "(LOWER(title) LIKE ? OR LOWER(name) LIKE ?)")
			pat := "%" + strings.ToLower(c.Value) + "%"
			args = append(args, pat, pat)
		case "group":
			where = append(where, "LOWER(group_name) LIKE ?")
			args = append(args, "%"+strings.ToLower(c.Value)+"%")
		case "handle":
			where = append(where, "LOWER(author) LIKE ?")
			args = append(args, "%"+strings.ToLower(c.Value)+"%")
		case "event", "party":
			where = append(where, "LOWER(party) LIKE ?")
			args = append(args, "%"+strings.ToLower(c.Value)+"%")
		case "year":
			if v, err := strconv.Atoi(c.Value); err == nil {
				switch c.Op {
				case ">=":
					where = append(where, "CAST(year AS INTEGER) >= ?")
				case ">":
					where = append(where, "CAST(year AS INTEGER) > ?")
				case "<=":
					where = append(where, "CAST(year AS INTEGER) <= ?")
				case "<":
					where = append(where, "CAST(year AS INTEGER) < ?")
				default:
					where = append(where, "year = ?")
				}
				args = append(args, v)
			}
		case "category":
			// Top-level category. Substring match on the path-style category
			// value mirrors Spiffy's filesystem-path matching.
			where = append(where, "category = ?")
			args = append(args, titleCase(c.Value))
		case "subcat":
			// Use the source field (CSDB/Gamebase/etc) as the sub-category.
			where = append(where, "source = ?")
			args = append(args, c.Value)
		case "type":
			where = append(where, "LOWER(file_type) = ?")
			args = append(args, strings.ToLower(c.Value))
		case "repo":
			where = append(where, "source = ?")
			args = append(args, c.Value)
		case "id":
			if v, err := strconv.ParseInt(c.Value, 10, 64); err == nil {
				where = append(where, "id = ?")
				args = append(args, v)
			}
		case "rating":
			if v, err := strconv.ParseFloat(c.Value, 64); err == nil {
				op := ">="
				if c.Op != "" {
					op = c.Op
				}
				where = append(where, "rating "+op+" ?")
				args = append(args, v)
			}
		case "sort":
			switch strings.ToLower(c.Value) {
			case "name":
				sortKey = "normalized_title"
			case "date", "updated":
				sortKey = "id" // newest-first proxy: highest id = most recently inserted
			case "year":
				sortKey = "year"
			}
		case "order":
			if strings.EqualFold(c.Value, "desc") {
				sortDir = "DESC"
			} else {
				sortDir = "ASC"
			}
		default:
			slog.Debug("spiffy: ignoring unknown AQL key", "key", c.Key, "value", c.Value)
		}
	}

	q := "SELECT id, category, source, title, name, normalized_title, group_name, year, path, primary_file, file_type, rating FROM entries"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY %s %s LIMIT ? OFFSET ?", sortKey, sortDir)
	args = append(args, count, start)

	rows, err := s.sqliteServer.DB().conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("spiffy search query: %w", err)
	}
	defer rows.Close()

	var out []*Entry
	for rows.Next() {
		e := &Entry{}
		var rating *float64
		if err := rows.Scan(
			&e.ID, &e.Category, &e.Source, &e.Title, &e.Name, &e.NormalizedTitle,
			&e.GroupName, &e.Year, &e.Path, &e.PrimaryFile, &e.FileType, &rating,
		); err != nil {
			return nil, err
		}
		e.Rating = rating
		out = append(out, e)
	}
	return out, rows.Err()
}

// titleCase capitalises the first letter ("games" → "Games") so the AQL
// dropdown values from our presets list match the entries.category values
// in the DB.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 32
	}
	return string(r)
}

// -----------------------------------------------------------------------------
// JSON helper.
// -----------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}
