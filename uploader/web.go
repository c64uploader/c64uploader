// Mobile-friendly web UI for the c64uploader server.
//
// Co-hosted with the Spiffy-compatible HTTP API (see spiffy.go) on the
// same -spiffy-http-port listener so a single port serves the Ultimate
// firmware's built-in Assembly64 browser AND a phone-browser UI.
//
// Routes (all under the same listener as /leet/search/*):
//
//   GET  /                         single-page web UI (HTML)
//   GET  /static/*                 CSS / JS (embedded)
//   GET  /api/menu?path=...        children of the given menu path
//   GET  /api/list?path=...&offset=...&limit=...&letter=...
//                                  entries at a path (paginated, optional letter)
//   GET  /api/search?q=...&cat=...&offset=...&limit=...
//                                  text search across entries
//   GET  /api/info/<id>            entry metadata
//   POST /api/run/<id>             upload + run the entry on the C64 via
//                                  the Ultimate REST API (PRG/CRT/D64/SID)
//
// The /api/* layer reuses the cart's existing HandleMenu / HandleListPath /
// HandleSearch / HandleInfo handlers to stay consistent — those return
// line-protocol strings which we parse back into JSON.

package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
)

//go:embed web/index.html web/static/*
var webAssets embed.FS

func (s *SpiffyServer) registerWebRoutes(mux *http.ServeMux) {
	staticFS, err := fs.Sub(webAssets, "web/static")
	if err == nil {
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	}
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/menu", s.apiMenu)
	mux.HandleFunc("/api/list", s.apiList)
	mux.HandleFunc("/api/search", s.apiSearch)
	mux.HandleFunc("/api/info/", s.apiInfo)
	mux.HandleFunc("/api/run/", s.apiRun)
}

// -----------------------------------------------------------------------------
// Index page.
// -----------------------------------------------------------------------------

func (s *SpiffyServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	body, err := webAssets.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

// -----------------------------------------------------------------------------
// /api/menu — children of a menu path.
// -----------------------------------------------------------------------------
//
// The cart menu protocol returns lines of "TYPE|NAME|PATH" terminated by ".".
// We parse them back into JSON so the JS UI doesn't need to know the wire
// format.

type webMenuItem struct {
	Type  string `json:"type"` // "f" folder, "l" list, "b" browse-letters
	Name  string `json:"name"`
	Path  string `json:"path"`
	Count int    `json:"count,omitempty"` // entry count from the cart protocol
}

type webMenuResponse struct {
	Path  string        `json:"path"`
	Items []webMenuItem `json:"items"`
}

func (s *SpiffyServer) apiMenu(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	resp := s.sqliteServer.HandleMenu(path)
	items := parseMenuItems(resp)
	writeJSON(w, webMenuResponse{Path: path, Items: items})
}

func parseMenuItems(resp string) []webMenuItem {
	out := []webMenuItem{}
	lines := strings.Split(resp, "\n")
	for i, ln := range lines {
		if i == 0 || ln == "." || ln == "" {
			continue
		}
		// Cart wire format: "type|name|path|count".
		fields := strings.SplitN(ln, "|", 4)
		if len(fields) < 2 {
			continue
		}
		item := webMenuItem{Type: fields[0], Name: fields[1]}
		if len(fields) >= 3 {
			item.Path = fields[2]
		}
		if len(fields) >= 4 {
			item.Count, _ = strconv.Atoi(strings.TrimSpace(fields[3]))
		}
		out = append(out, item)
	}
	return out
}

// -----------------------------------------------------------------------------
// /api/list — entry list at a path (paginated, grouped by title).
// -----------------------------------------------------------------------------
//
// Reuses HandleListPath which returns "ID|Title|Category|ReleaseCount|Trainers".

type webEntry struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Category     string `json:"category,omitempty"`
	ReleaseCount int    `json:"release_count,omitempty"`
	Trainers     int    `json:"trainers,omitempty"`
}

type webEntryListResponse struct {
	Path    string     `json:"path"`
	Offset  int        `json:"offset"`
	Total   int        `json:"total"`
	Entries []webEntry `json:"entries"`
}

func (s *SpiffyServer) apiList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	path := q.Get("path")
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	letter := q.Get("letter")

	resp := s.sqliteServer.HandleListPath(path, offset, limit, letter)
	total, entries := parseEntryList(resp)
	writeJSON(w, webEntryListResponse{Path: path, Offset: offset, Total: total, Entries: entries})
}

// parseEntryList parses a HandleListPath / HandleSearch line-protocol response.
// Header is "OK <returned> <total>" and each row is "ID|Title|Category|Count|Trainers".
func parseEntryList(resp string) (total int, entries []webEntry) {
	entries = []webEntry{}
	lines := strings.Split(resp, "\n")
	for i, ln := range lines {
		if ln == "." || ln == "" {
			continue
		}
		if i == 0 {
			parts := strings.Fields(ln)
			if len(parts) >= 3 {
				total, _ = strconv.Atoi(parts[2])
			}
			continue
		}
		fields := strings.SplitN(ln, "|", 5)
		if len(fields) < 2 {
			continue
		}
		var e webEntry
		e.ID, _ = strconv.ParseInt(fields[0], 10, 64)
		e.Name = fields[1]
		if len(fields) >= 3 {
			e.Category = fields[2]
		}
		if len(fields) >= 4 {
			e.ReleaseCount, _ = strconv.Atoi(fields[3])
		}
		if len(fields) >= 5 {
			e.Trainers, _ = strconv.Atoi(fields[4])
		}
		entries = append(entries, e)
	}
	return total, entries
}

// -----------------------------------------------------------------------------
// /api/search — text search.
// -----------------------------------------------------------------------------

func (s *SpiffyServer) apiSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := strings.TrimSpace(q.Get("q"))
	cat := q.Get("cat")
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if query == "" {
		writeJSON(w, webEntryListResponse{Entries: []webEntry{}})
		return
	}
	resp := s.sqliteServer.HandleSearch(query, cat, offset, limit)
	total, entries := parseEntryList(resp)
	writeJSON(w, webEntryListResponse{Path: "search:" + query, Offset: offset, Total: total, Entries: entries})
}

// -----------------------------------------------------------------------------
// /api/info/<id>
// -----------------------------------------------------------------------------

type webInfoResponse struct {
	ID          int64   `json:"id"`
	Title       string  `json:"title"`
	Group       string  `json:"group,omitempty"`
	Year        string  `json:"year,omitempty"`
	Category    string  `json:"category"`
	Source      string  `json:"source"`
	FileType    string  `json:"type,omitempty"`
	PrimaryFile string  `json:"primary_file,omitempty"`
	Path        string  `json:"path"`
	Top200      int     `json:"top200,omitempty"`
	Rating      float64 `json:"rating,omitempty"`
	Trainers    int     `json:"trainers,omitempty"`
	ReleaseName string  `json:"release_name,omitempty"`
	Author      string  `json:"author,omitempty"`
}

func (s *SpiffyServer) apiInfo(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/info/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	e, err := s.sqliteServer.DB().GetEntryByID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if e == nil {
		http.NotFound(w, r)
		return
	}
	out := webInfoResponse{
		ID:          e.ID,
		Title:       e.Title,
		Group:       e.GroupName,
		Year:        e.Year,
		Category:    e.Category,
		Source:      e.Source,
		FileType:    e.FileType,
		PrimaryFile: e.PrimaryFile,
		Path:        e.Path,
		Trainers:    e.Trainers,
		ReleaseName: e.ReleaseName,
		Author:      e.Author,
	}
	if e.Top200Rank != nil {
		out.Top200 = *e.Top200Rank
	}
	if e.Rating != nil {
		out.Rating = *e.Rating
	}
	writeJSON(w, out)
}

// -----------------------------------------------------------------------------
// /api/run/<id>
// -----------------------------------------------------------------------------

type webRunResponse struct {
	OK   bool   `json:"ok"`
	Type string `json:"type,omitempty"`
	File string `json:"file,omitempty"`
	Err  string `json:"error,omitempty"`
}

func (s *SpiffyServer) apiRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/run/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	filePath, err := s.sqliteServer.GetFilePath(id)
	if err != nil {
		writeJSON(w, webRunResponse{Err: err.Error()})
		return
	}
	fileData, err := os.ReadFile(filePath)
	if err != nil {
		writeJSON(w, webRunResponse{Err: fmt.Sprintf("read failed: %v", err)})
		return
	}
	if err := uploadAndRunFile(s.apiClient, fileData, filePath); err != nil {
		slog.Warn("web run failed", "id", id, "path", filePath, "err", err)
		writeJSON(w, webRunResponse{Err: err.Error(), File: filePath})
		return
	}
	slog.Info("web run", "id", id, "path", filePath)
	writeJSON(w, webRunResponse{OK: true, Type: detectFileType(filePath), File: filePath})
}

// -----------------------------------------------------------------------------
// Helpers.
// -----------------------------------------------------------------------------

// jsonError writes a JSON error response with the given HTTP status.
//
//nolint:unused // reserved for future API expansion
func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
