// Package httpserver is the HTTP control plane for a directory of MedGit records.
//
// It does not authenticate. Put a proxy in front before it can see anything but the demo.
package httpserver

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/aomarnevarez/medgit/internal/canonical"
	"github.com/aomarnevarez/medgit/internal/hl7"
	"github.com/aomarnevarez/medgit/internal/mederr"
	"github.com/aomarnevarez/medgit/internal/repo"
)

const maxBody = 1_000_000

var recordID = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,64}$`)

// Server serves one directory of records.
type Server struct {
	DataRoot string
}

// EnsureDemo builds the f001 record when it is not already on disk.
func (s *Server) EnsureDemo() error {
	root := filepath.Join(s.DataRoot, "f001")
	if info, err := os.Stat(filepath.Join(root, "medgit.json")); err == nil && !info.IsDir() {
		return nil
	}
	if err := os.MkdirAll(s.DataRoot, 0o755); err != nil {
		return err
	}
	_, err := hl7.BuildF001(root, nil)
	return err
}

// ServeHTTP implements the control-plane routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	defer func() {
		if rec := recover(); rec != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprint(rec)})
		}
	}()
	switch r.Method {
	case http.MethodGet:
		s.get(w, path)
	case http.MethodPost:
		s.post(w, r, path)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

func (s *Server) get(w http.ResponseWriter, path string) {
	if path == "/health" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "medgit": repo.Version})
		return
	}
	if path == "/records" {
		names := []string{}
		entries, err := os.ReadDir(s.DataRoot)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				if info, err := os.Stat(filepath.Join(s.DataRoot, entry.Name(), "medgit.json")); err == nil && !info.IsDir() {
					names = append(names, entry.Name())
				}
			}
			sort.Strings(names)
		}
		writeJSON(w, http.StatusOK, map[string]any{"records": names})
		return
	}
	record, rest, ok := splitPath(path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	opened := repo.Open(filepath.Join(s.DataRoot, record))
	var payload any
	var err error
	switch rest {
	case "":
		payload, err = opened.Status()
	case "/log":
		rows, logErr := opened.Log("main", true)
		if logErr != nil {
			err = logErr
			break
		}
		brief := make([]any, len(rows))
		for i, row := range rows {
			brief[i] = briefRow(row)
		}
		payload = brief
	case "/verify":
		payload, err = opened.Verify()
	case "/fhir":
		payload, err = opened.ExportFHIR("")
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) post(w http.ResponseWriter, r *http.Request, path string) {
	body, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if path == "/records" {
		s.create(w, body)
		return
	}
	record, rest, ok := splitPath(path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	opened := repo.Open(filepath.Join(s.DataRoot, record))
	actor, err := actorFrom(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	switch rest {
	case "/episode":
		reason := asString(body["reason"])
		if reason == "" {
			reason = "episode"
		}
		created, err := opened.OpenEpisode(actor, reason, "", "", nil)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"episode_id": created["episode_id"], "hash": created["hash"]})
	case "/commit":
		episodeID := asString(body["episode_id"])
		if episodeID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing episode_id"})
			return
		}
		commitType := asString(body["type"])
		if commitType == "" {
			commitType = "note"
		}
		message := asString(body["message"])
		if message == "" {
			message = "commit"
		}
		resources := asSlice(body["resources"])
		if resources == nil {
			resources = []any{}
		}
		committed, err := opened.Commit(actor, episodeID, commitType, message, resources, "", "", "")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"hash": committed["hash"]})
	case "/consult":
		episodeID := asString(body["episode_id"])
		if episodeID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing episode_id"})
			return
		}
		title := asString(body["title"])
		if title == "" {
			title = "consult"
		}
		openedPull, err := opened.OpenPull(actor, episodeID, title, asString(body["body"]))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": openedPull["id"], "state": openedPull["state"]})
	case "/review":
		prID := asString(body["pr_id"])
		if prID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing pr_id"})
			return
		}
		decision := asString(body["decision"])
		if decision == "" {
			decision = "comment"
		}
		reviewed, err := opened.Review(actor, prID, decision, asString(body["comment"]))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": reviewed["id"], "state": reviewed["state"]})
	case "/merge":
		prID := asString(body["pr_id"])
		if prID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing pr_id"})
			return
		}
		acknowledge, _ := body["acknowledge_conflicts"].(bool)
		merged, err := opened.Merge(actor, prID, acknowledge)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"hash": merged["hash"]})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

func (s *Server) create(w http.ResponseWriter, body map[string]any) {
	patient, ok := body["patient"].(map[string]any)
	if !ok || asString(patient["resourceType"]) != "Patient" || asString(patient["id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "patient must be a FHIR Patient with an id"})
		return
	}
	recordIDText := asString(patient["id"])
	if !recordID.MatchString(recordIDText) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "patient id is not a safe record id"})
		return
	}
	target := filepath.Join(s.DataRoot, recordIDText)
	if info, err := os.Stat(filepath.Join(target, "medgit.json")); err == nil && !info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "record already exists"})
		return
	}
	actor, err := actorFrom(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	names := asSlice(patient["name"])
	if len(names) == 0 {
		names = []any{map[string]any{}}
	}
	display := recordIDText
	if first, ok := names[0].(map[string]any); ok && asString(first["text"]) != "" {
		display = asString(first["text"])
	}
	gender := asString(patient["gender"])
	if gender == "" {
		gender = "unknown"
	}
	birthDate := asString(patient["birthDate"])
	if birthDate == "" {
		birthDate = "1970-01-01"
	}
	if err := os.MkdirAll(s.DataRoot, 0o755); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	record, err := repo.Init(target, actor, recordIDText, display, gender, birthDate, nil, []any{patient})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	var episodeID any
	for _, item := range asSlice(body["resources"]) {
		resource, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if asString(resource["resourceType"]) == "Encounter" && episodeID == nil {
			encounterID := asString(resource["id"])
			if encounterID == "" {
				encounterID = "imported"
			}
			opened, err := record.OpenEpisode(actor, "imported encounter", "", encounterID, []any{resource})
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			episodeID = opened["episode_id"]
			continue
		}
		if episodeID == nil {
			opened, err := record.OpenEpisode(actor, "imported resources", "", "", nil)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			episodeID = opened["episode_id"]
		}
		if _, err := hl7.CommitResource(record, actor, asString(episodeID), resource); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": recordIDText, "episode_id": episodeID})
}

func readBody(r *http.Request) (map[string]any, error) {
	length := r.ContentLength
	if length < 0 {
		length = 0
	}
	if length > maxBody {
		return nil, mederr.Errorf("body too large")
	}
	if length == 0 {
		return map[string]any{}, nil
	}
	raw := make([]byte, length)
	if _, err := io.ReadFull(r.Body, raw); err != nil {
		return nil, err
	}
	decoded, err := canonical.Decode(raw)
	if err != nil {
		return nil, err
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, mederr.Errorf("JSON object required")
	}
	return object, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	raw, err := canonical.Marshal(payload)
	if err != nil {
		raw = []byte(`{"error":"encode failed"}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func splitPath(path string) (string, string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] != "records" || !recordID.MatchString(parts[1]) {
		return "", "", false
	}
	rest := ""
	if len(parts) > 2 {
		rest = "/" + strings.Join(parts[2:], "/")
	}
	return parts[1], rest, true
}

func actorFrom(body map[string]any) (repo.Actor, error) {
	raw := asMap(body["actor"])
	id := asString(raw["id"])
	if id == "" {
		id = "att-demo"
	}
	display := asString(raw["display"])
	if display == "" {
		display = "Attending (demo operator)"
	}
	role := asString(raw["role"])
	if role == "" {
		role = "attending"
	}
	return repo.NewActor(id, display, role)
}

func briefRow(row map[string]any) map[string]any {
	return map[string]any{
		"hash":        row["hash"],
		"type":        row["type"],
		"message":     row["message"],
		"author":      asMap(row["author"])["display"],
		"recorded_at": row["recorded_at"],
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
