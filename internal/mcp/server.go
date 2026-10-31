// Package mcp is a minimal MCP server over stdio with Content-Length framing.
//
// Write tools stay off unless MEDGIT_ALLOW_WRITE=1. The actor still comes from
// the caller; this process does not authenticate anyone.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aomarnevarez/medgit/internal/canonical"
	"github.com/aomarnevarez/medgit/internal/hl7"
	"github.com/aomarnevarez/medgit/internal/mederr"
	"github.com/aomarnevarez/medgit/internal/repo"
)

const protocol = "2024-11-05"

// Server answers MCP requests for one record directory.
type Server struct {
	Root string
}

// Serve reads framed messages until stdin closes.
func Serve(r io.Reader, w io.Writer, root string) error {
	server := &Server{Root: root}
	reader := bufio.NewReader(r)
	for {
		message, err := ReadMessage(reader)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if message == nil {
			return nil
		}
		response := server.Handle(message)
		if response == nil {
			continue
		}
		if err := WriteMessage(w, response); err != nil {
			return err
		}
	}
}

// Handle answers one JSON-RPC message. Notifications return nil.
func (s *Server) Handle(message map[string]any) map[string]any {
	method := asString(message["method"])
	if message["id"] == nil && method != "" && strings.HasPrefix(method, "notifications/") {
		return nil
	}
	requestID := message["id"]
	switch method {
	case "initialize":
		return rpcResult(requestID, map[string]any{
			"protocolVersion": protocol,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "medgit", "version": repo.Version},
		})
	case "tools/list":
		return rpcResult(requestID, map[string]any{"tools": toolDefs()})
	case "tools/call":
		params := asMap(message["params"])
		arguments := asMap(params["arguments"])
		result, err := s.call(asString(params["name"]), arguments)
		if err != nil {
			return rpcResult(requestID, toolText(err.Error(), true))
		}
		return rpcResult(requestID, result)
	case "ping":
		return rpcResult(requestID, map[string]any{})
	default:
		return rpcError(requestID, -32601, fmt.Sprintf("unknown method %s", method))
	}
}

func (s *Server) call(name string, arguments map[string]any) (map[string]any, error) {
	record := repo.Open(s.Root)
	switch name {
	case "medgit_status":
		status, err := record.Status()
		if err != nil {
			return nil, err
		}
		text, err := pretty(status)
		if err != nil {
			return nil, err
		}
		return toolText(text, false), nil
	case "medgit_log":
		ref := asString(arguments["ref"])
		if ref == "" {
			ref = "main"
		}
		all, _ := arguments["all_parents"].(bool)
		rows, err := record.Log(ref, all)
		if err != nil {
			return nil, err
		}
		brief := make([]any, len(rows))
		for i, row := range rows {
			brief[i] = map[string]any{
				"hash":        row["hash"],
				"type":        row["type"],
				"message":     row["message"],
				"author":      asMap(row["author"])["display"],
				"recorded_at": row["recorded_at"],
			}
		}
		text, err := pretty(brief)
		if err != nil {
			return nil, err
		}
		return toolText(text, false), nil
	case "medgit_verify":
		report, err := record.Verify()
		if err != nil {
			return nil, err
		}
		text, err := pretty(report)
		if err != nil {
			return nil, err
		}
		return toolText(text, false), nil
	case "medgit_export_fhir":
		bundle, err := record.ExportFHIR("")
		if err != nil {
			return nil, err
		}
		text, err := pretty(bundle)
		if err != nil {
			return nil, err
		}
		return toolText(text, false), nil
	case "medgit_qsofa":
		if err := requireWrite(); err != nil {
			return nil, err
		}
		actor, err := actorFrom(arguments)
		if err != nil {
			return nil, err
		}
		episodeID := asString(arguments["episode_id"])
		rr, err := asInt(arguments["respiratory_rate"])
		if err != nil {
			return nil, err
		}
		sbp, err := asInt(arguments["systolic_bp"])
		if err != nil {
			return nil, err
		}
		altered, ok := arguments["altered_mentation"].(bool)
		if !ok {
			return nil, mederr.Errorf("altered_mentation must be true or false")
		}
		committed, err := record.CommitQSOFA(actor, episodeID, rr, sbp, altered, "", "")
		if err != nil {
			return nil, err
		}
		return toolText(asString(committed["hash"])+" "+asString(committed["message"]), false), nil
	case "medgit_load_demo":
		if err := requireWrite(); err != nil {
			return nil, err
		}
		if info, err := os.Stat(filepath.Join(s.Root, "medgit.json")); err == nil && !info.IsDir() {
			return nil, mederr.Errorf("record already exists; refusing to overwrite it")
		}
		built, err := hl7.BuildF001(s.Root, nil)
		if err != nil {
			return nil, err
		}
		report, err := built.Verify()
		if err != nil {
			return nil, err
		}
		config, err := built.Config()
		if err != nil {
			return nil, err
		}
		text, err := pretty(map[string]any{"patient": config["display"], "verify": report})
		if err != nil {
			return nil, err
		}
		return toolText(text, false), nil
	case "medgit_commit_fhir":
		if err := requireWrite(); err != nil {
			return nil, err
		}
		resource, ok := arguments["resource"].(map[string]any)
		if !ok {
			return nil, mederr.Errorf("resource must be a FHIR object")
		}
		actor, err := actorFrom(arguments)
		if err != nil {
			return nil, err
		}
		episodeID := asString(arguments["episode_id"])
		if episodeID == "" {
			return nil, mederr.Errorf("missing episode_id")
		}
		committed, err := hl7.CommitResource(record, actor, episodeID, resource)
		if err != nil {
			return nil, err
		}
		return toolText(asString(committed["hash"])+" "+asString(committed["message"]), false), nil
	default:
		return nil, mederr.Errorf("unknown tool %s", name)
	}
}

// ReadMessage parses one LSP-style framed JSON object.
func ReadMessage(r *bufio.Reader) (map[string]any, error) {
	headers := map[string]string{}
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if err == io.EOF && len(line) == 0 {
				return nil, io.EOF
			}
			return nil, err
		}
		if string(line) == "\r\n" || string(line) == "\n" {
			break
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			break
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		headers[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	length, _ := strconv.Atoi(headers["content-length"])
	if length <= 0 {
		return nil, io.EOF
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	decoded, err := canonical.Decode(body)
	if err != nil {
		return nil, err
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, mederr.Errorf("JSON object required")
	}
	return object, nil
}

// WriteMessage writes one framed JSON object.
func WriteMessage(w io.Writer, payload any) error {
	raw, err := canonical.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(raw)); err != nil {
		return err
	}
	_, err = w.Write(raw)
	return err
}

func toolDefs() []any {
	return []any{
		tool("medgit_status", "Patient, episodes, and open consults.", map[string]any{}, nil),
		tool("medgit_log", "First-parent log, or the full ancestor set when all_parents is true.", map[string]any{
			"ref":         map[string]any{"type": "string"},
			"all_parents": map[string]any{"type": "boolean"},
		}, nil),
		tool("medgit_verify", "Rehash every object and check the parent chain.", map[string]any{}, nil),
		tool("medgit_export_fhir", "Project the longitudinal record to a FHIR collection Bundle.", map[string]any{}, nil),
		tool("medgit_qsofa", "Commit a qSOFA screening result. Requires MEDGIT_ALLOW_WRITE=1.", map[string]any{
			"episode_id":        map[string]any{"type": "string"},
			"respiratory_rate":  map[string]any{"type": "integer"},
			"systolic_bp":       map[string]any{"type": "integer"},
			"altered_mentation": map[string]any{"type": "boolean"},
			"actor_id":          map[string]any{"type": "string"},
			"actor_display":     map[string]any{"type": "string"},
			"actor_role":        map[string]any{"type": "string"},
		}, []any{"episode_id", "respiratory_rate", "systolic_bp", "altered_mentation", "actor_id", "actor_display", "actor_role"}),
		tool("medgit_load_demo", "Build the official HL7 FHIR R4 f001 record in MEDGIT_REPO. Refuses if a record is already there. Requires MEDGIT_ALLOW_WRITE=1.", map[string]any{}, nil),
		tool("medgit_commit_fhir", "Append one FHIR resource to an open episode. Requires MEDGIT_ALLOW_WRITE=1. Does not merge.", map[string]any{
			"episode_id":    map[string]any{"type": "string"},
			"resource":      map[string]any{"type": "object"},
			"actor_id":      map[string]any{"type": "string"},
			"actor_display": map[string]any{"type": "string"},
			"actor_role":    map[string]any{"type": "string"},
		}, []any{"episode_id", "resource", "actor_id", "actor_display", "actor_role"}),
	}
}

func tool(name, description string, properties map[string]any, required []any) map[string]any {
	if required == nil {
		required = []any{}
	}
	return map[string]any{
		"name":        name,
		"description": description,
		"inputSchema": map[string]any{
			"type":                 "object",
			"properties":           properties,
			"required":             required,
			"additionalProperties": false,
		},
	}
}

func actorFrom(arguments map[string]any) (repo.Actor, error) {
	id, ok1 := arguments["actor_id"].(string)
	display, ok2 := arguments["actor_display"].(string)
	role, ok3 := arguments["actor_role"].(string)
	if !ok1 || !ok2 || !ok3 {
		return repo.Actor{}, mederr.Errorf("actor_id, actor_display, and actor_role are required")
	}
	return repo.NewActor(id, display, role)
}

func requireWrite() error {
	if os.Getenv("MEDGIT_ALLOW_WRITE") != "1" {
		return mederr.Errorf("write tools are disabled; set MEDGIT_ALLOW_WRITE=1")
	}
	return nil
}

func toolText(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": isError,
	}
}

func rpcResult(id, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func rpcError(id any, code int, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	}
}

func pretty(v any) (string, error) {
	raw, err := canonical.MarshalIndent(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func asInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, err
		}
		return int(i), nil
	default:
		return 0, mederr.Errorf("expected integer")
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
