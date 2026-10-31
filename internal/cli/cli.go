// Package cli is the medgit command line.
//
// The CLI trusts the actor you pass. A deployed system has to authenticate that
// actor before it calls the library.
package cli

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/aomarnevarez/medgit/internal/canonical"
	"github.com/aomarnevarez/medgit/internal/hl7"
	"github.com/aomarnevarez/medgit/internal/httpserver"
	"github.com/aomarnevarez/medgit/internal/mcp"
	"github.com/aomarnevarez/medgit/internal/mederr"
	"github.com/aomarnevarez/medgit/internal/repo"
)

// Main runs the CLI with the process standard streams.
func Main(argv []string) int {
	return Run(argv, os.Stdout, os.Stderr)
}

// Run executes argv and writes human or JSON output to stdout.
func Run(argv []string, stdout, stderr io.Writer) int {
	path, asJSON, rest, explicitRepo, err := splitGlobals(argv)
	if err != nil {
		fmt.Fprintf(stderr, "medgit: %s\n", err)
		return 2
	}
	if len(rest) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	cmd := rest[0]
	args := rest[1:]
	payload, runErr := dispatch(cmd, args, path, explicitRepo, stdout, stderr)
	if runErr != nil {
		if usage, ok := runErr.(errUsage); ok {
			if usage.error != nil {
				fmt.Fprintf(stderr, "medgit: %s\n", usage.Error())
			}
			return 2
		}
		var rewrite *mederr.Rewrite
		if ok := asRewrite(runErr, &rewrite); ok {
			fmt.Fprintf(stderr, "medgit: refused: %s\n", rewrite.Error())
			return 2
		}
		fmt.Fprintf(stderr, "medgit: %s\n", runErr.Error())
		return 1
	}
	if payload == nil && (cmd == "serve" || cmd == "mcp" || cmd == "amend" || cmd == "rebase" || cmd == "reset") {
		return 0
	}
	if asJSON {
		raw, err := canonical.MarshalIndent(payload)
		if err != nil {
			fmt.Fprintf(stderr, "medgit: %s\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(raw))
		return 0
	}
	if err := printHuman(stdout, cmd, payload); err != nil {
		fmt.Fprintf(stderr, "medgit: %s\n", err)
		return 1
	}
	return 0
}

func dispatch(cmd string, args []string, path string, explicitRepo bool, stdout, stderr io.Writer) (any, error) {
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(stderr, usageText)
		return nil, errUsage{}
	case "serve":
		return nil, serve(stdout, stderr)
	case "mcp":
		return nil, serveMCP(path, explicitRepo)
	case "demo":
		record, err := hl7.BuildF001(path, nil)
		if err != nil {
			return nil, err
		}
		report, err := record.Verify()
		if err != nil {
			return nil, err
		}
		if err := requireOK(report); err != nil {
			return nil, err
		}
		config, err := record.Config()
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": record.Root(), "patient": config["display"], "verify": report}, nil
	case "init":
		return cmdInit(path, args, explicitRepo)
	case "amend", "rebase", "reset":
		record := repo.Open(path)
		switch cmd {
		case "amend":
			return nil, record.Amend()
		case "rebase":
			return nil, record.Rebase()
		default:
			return nil, record.Reset()
		}
	case "episode":
		return cmdEpisode(path, args)
	case "commit":
		return cmdCommit(path, args)
	case "note":
		return cmdNote(path, args)
	case "observe":
		return cmdObserve(path, args)
	case "qsofa":
		return cmdQSOFA(path, args)
	case "consult":
		return cmdConsult(path, args)
	case "abandon":
		return cmdAbandon(path, args)
	case "log":
		return cmdLog(path, args)
	case "status":
		return repo.Open(path).Status()
	case "verify":
		report, err := repo.Open(path).Verify()
		if err != nil {
			return nil, err
		}
		if err := requireOK(report); err != nil {
			return nil, err
		}
		return report, nil
	case "export-fhir":
		return cmdExport(path, args)
	case "show":
		return cmdShow(path, args)
	default:
		return nil, mederr.Errorf("unknown command %s", cmd)
	}
}

func cmdInit(path string, args []string, explicitRepo bool) (any, error) {
	fs := newFlag("init")
	patientRef := fs.String("patient-ref", "", "patient identifier")
	display := fs.String("display", "", "patient display name")
	gender := fs.String("gender", "unknown", "administrative gender")
	birthDate := fs.String("birth-date", "1978-04-12", "birth date")
	id, actorDisplay, role := actorFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return nil, err
	}
	if fs.NArg() > 1 {
		return nil, errUsage{mederr.Errorf("init takes at most one directory")}
	}
	if fs.NArg() == 1 {
		if explicitRepo && path != fs.Arg(0) {
			return nil, errUsage{mederr.Errorf("init was given two directories, %s and %s", path, fs.Arg(0))}
		}
		path = fs.Arg(0)
	}
	actor, err := requireActor(*id, *actorDisplay, *role)
	if err != nil {
		return nil, err
	}
	if *patientRef == "" || *display == "" {
		return nil, errUsage{mederr.Errorf("init requires --patient-ref and --display")}
	}
	record, err := repo.Init(path, actor, *patientRef, *display, *gender, *birthDate, nil, nil)
	if err != nil {
		return nil, err
	}
	status, err := record.Status()
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": record.Root(), "main": status["main"]}, nil
}

func cmdEpisode(path string, args []string) (any, error) {
	if len(args) == 0 || args[0] != "open" {
		return nil, errUsage{mederr.Errorf("usage: medgit episode open --reason TEXT")}
	}
	fs := newFlag("episode open")
	reason := fs.String("reason", "", "clinical reason")
	class := fs.String("class", "EMER", "encounter class")
	id, display, role := actorFlags(fs)
	if err := parseArgs(fs, args[1:]); err != nil {
		return nil, err
	}
	actor, err := requireActor(*id, *display, *role)
	if err != nil {
		return nil, err
	}
	if *reason == "" {
		return nil, errUsage{mederr.Errorf("episode open requires --reason")}
	}
	return repo.Open(path).OpenEpisode(actor, *reason, *class, "", nil)
}

func cmdCommit(path string, args []string) (any, error) {
	fs := newFlag("commit")
	episode := fs.String("episode", "", "episode id")
	commitType := fs.String("type", "", "commit type")
	message := fs.String("message", "", "commit message")
	resource := fs.String("resource", "", "path to a JSON resource or array")
	sensitivity := fs.String("sensitivity", "normal", "normal or restricted")
	corrects := fs.String("corrects", "", "hash of the commit this correction replaces")
	id, display, role := actorFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return nil, err
	}
	actor, err := requireActor(*id, *display, *role)
	if err != nil {
		return nil, err
	}
	if *episode == "" || *commitType == "" || *message == "" || *resource == "" {
		return nil, errUsage{mederr.Errorf("commit requires --episode, --type, --message, and --resource")}
	}
	raw, err := os.ReadFile(*resource)
	if err != nil {
		return nil, err
	}
	decoded, err := canonical.DecodeStrict(raw)
	if err != nil {
		return nil, err
	}
	var resources []any
	switch value := decoded.(type) {
	case []any:
		resources = value
	case map[string]any:
		resources = []any{value}
	default:
		return nil, mederr.Errorf("resource file must be a JSON object or array")
	}
	return repo.Open(path).Commit(actor, *episode, *commitType, *message, resources, *sensitivity, *corrects, "")
}

func cmdNote(path string, args []string) (any, error) {
	fs := newFlag("note")
	episode := fs.String("episode", "", "episode id")
	text := fs.String("text", "", "note text")
	id, display, role := actorFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return nil, err
	}
	actor, err := requireActor(*id, *display, *role)
	if err != nil {
		return nil, err
	}
	if *episode == "" || *text == "" {
		return nil, errUsage{mederr.Errorf("note requires --episode and --text")}
	}
	return repo.Open(path).CommitNote(actor, *episode, *text, "")
}

func cmdObserve(path string, args []string) (any, error) {
	fs := newFlag("observe")
	episode := fs.String("episode", "", "episode id")
	loinc := fs.String("loinc", "", "LOINC code")
	displayName := fs.String("display", "", "observation display")
	value := fs.String("value", "", "decimal string")
	unit := fs.String("unit", "", "unit text")
	ucum := fs.String("ucum", "", "UCUM code")
	message := fs.String("message", "", "commit message")
	id, display, role := actorFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return nil, err
	}
	actor, err := requireActor(*id, *display, *role)
	if err != nil {
		return nil, err
	}
	if *episode == "" || *loinc == "" || *displayName == "" || *value == "" || *unit == "" || *ucum == "" {
		return nil, errUsage{mederr.Errorf("observe requires --episode, --loinc, --display, --value, --unit, and --ucum")}
	}
	return repo.Open(path).CommitObservation(actor, *episode, *loinc, *displayName, *value, *unit, *ucum, *message, "", "")
}

func cmdQSOFA(path string, args []string) (any, error) {
	fs := newFlag("qsofa")
	episode := fs.String("episode", "", "episode id")
	rr := fs.Int("rr", 0, "respiratory rate")
	sbp := fs.Int("sbp", 0, "systolic blood pressure")
	altered := fs.Bool("altered", false, "altered mentation")
	id, display, role := actorFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return nil, err
	}
	actor, err := requireActor(*id, *display, *role)
	if err != nil {
		return nil, err
	}
	rrSet, sbpSet := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "rr":
			rrSet = true
		case "sbp":
			sbpSet = true
		}
	})
	if *episode == "" || !rrSet || !sbpSet {
		return nil, errUsage{mederr.Errorf("qsofa requires --episode, --rr, and --sbp")}
	}
	return repo.Open(path).CommitQSOFA(actor, *episode, *rr, *sbp, *altered, "", "")
}

func cmdConsult(path string, args []string) (any, error) {
	if len(args) == 0 {
		return nil, errUsage{mederr.Errorf("usage: medgit consult open|review|refresh|merge")}
	}
	record := repo.Open(path)
	switch args[0] {
	case "open":
		fs := newFlag("consult open")
		episode := fs.String("episode", "", "episode id")
		title := fs.String("title", "", "consult title")
		body := fs.String("body", "", "consult body")
		id, display, role := actorFlags(fs)
		if err := parseArgs(fs, args[1:]); err != nil {
			return nil, err
		}
		actor, err := requireActor(*id, *display, *role)
		if err != nil {
			return nil, err
		}
		if *episode == "" || *title == "" {
			return nil, errUsage{mederr.Errorf("consult open requires --episode and --title")}
		}
		return record.OpenPull(actor, *episode, *title, *body)
	case "review":
		fs := newFlag("consult review")
		decision := fs.String("decision", "", "approve, request-changes, or comment")
		comment := fs.String("comment", "", "review comment")
		id, display, role := actorFlags(fs)
		if err := parseArgs(fs, args[1:]); err != nil {
			return nil, err
		}
		if fs.NArg() != 1 || *decision == "" {
			return nil, errUsage{mederr.Errorf("consult review requires a consult id and --decision")}
		}
		actor, err := requireActor(*id, *display, *role)
		if err != nil {
			return nil, err
		}
		return record.Review(actor, fs.Arg(0), *decision, *comment)
	case "refresh":
		fs := newFlag("consult refresh")
		id, display, role := actorFlags(fs)
		if err := parseArgs(fs, args[1:]); err != nil {
			return nil, err
		}
		if fs.NArg() != 1 {
			return nil, errUsage{mederr.Errorf("consult refresh requires a consult id")}
		}
		actor, err := requireActor(*id, *display, *role)
		if err != nil {
			return nil, err
		}
		return record.RefreshPull(actor, fs.Arg(0))
	case "merge":
		fs := newFlag("consult merge")
		acknowledge := fs.Bool("acknowledge-conflicts", false, "acknowledge discordant observations")
		id, display, role := actorFlags(fs)
		if err := parseArgs(fs, args[1:]); err != nil {
			return nil, err
		}
		if fs.NArg() != 1 {
			return nil, errUsage{mederr.Errorf("consult merge requires a consult id")}
		}
		actor, err := requireActor(*id, *display, *role)
		if err != nil {
			return nil, err
		}
		return record.Merge(actor, fs.Arg(0), *acknowledge)
	default:
		return nil, errUsage{mederr.Errorf("unknown consult command %s", args[0])}
	}
}

func cmdAbandon(path string, args []string) (any, error) {
	fs := newFlag("abandon")
	episode := fs.String("episode", "", "episode id")
	reason := fs.String("reason", "", "why the episode is abandoned")
	id, display, role := actorFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return nil, err
	}
	actor, err := requireActor(*id, *display, *role)
	if err != nil {
		return nil, err
	}
	if *episode == "" || *reason == "" {
		return nil, errUsage{mederr.Errorf("abandon requires --episode and --reason")}
	}
	return repo.Open(path).Abandon(actor, *episode, *reason)
}

func cmdLog(path string, args []string) (any, error) {
	fs := newFlag("log")
	ref := fs.String("ref", "main", "ref name")
	all := fs.Bool("all", false, "follow every parent")
	if err := parseArgs(fs, args); err != nil {
		return nil, err
	}
	rows, err := repo.Open(path).Log(*ref, *all)
	if err != nil {
		return nil, err
	}
	brief := make([]map[string]any, len(rows))
	for i, item := range rows {
		author := asMap(item["author"])
		brief[i] = map[string]any{
			"hash":        item["hash"],
			"type":        item["type"],
			"author":      author["display"],
			"role":        author["role"],
			"recorded_at": item["recorded_at"],
			"message":     item["message"],
			"episode_id":  item["episode_id"],
		}
	}
	return brief, nil
}

func cmdExport(path string, args []string) (any, error) {
	fs := newFlag("export-fhir")
	ref := fs.String("ref", "main", "ref name")
	if err := parseArgs(fs, args); err != nil {
		return nil, err
	}
	return repo.Open(path).ExportFHIR(*ref)
}

func cmdShow(path string, args []string) (any, error) {
	if len(args) != 1 {
		return nil, errUsage{mederr.Errorf("show requires an object id")}
	}
	document, err := repo.Open(path).Object(args[0])
	if err != nil {
		return nil, err
	}
	document["hash"] = args[0]
	return document, nil
}

func serve(stdout, stderr io.Writer) error {
	root := os.Getenv("MEDGIT_DATA")
	if root == "" {
		root = "./data"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	server := &httpserver.Server{DataRoot: root}
	if err := server.EnsureDemo(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "medgit %s listening on %s\n", repo.Version, port)
	return http.ListenAndServe("0.0.0.0:"+port, server)
}

func serveMCP(path string, explicit bool) error {
	root := path
	if !explicit {
		if env := os.Getenv("MEDGIT_REPO"); env != "" {
			root = env
		}
	}
	if root == "" {
		root = "."
	}
	return mcp.Serve(os.Stdin, os.Stdout, root)
}

func printHuman(w io.Writer, cmd string, payload any) error {
	switch cmd {
	case "log":
		rows, ok := payload.([]map[string]any)
		if !ok {
			break
		}
		for _, item := range rows {
			fmt.Fprintf(w, "%s  %-16s %-10s %s  %s\n",
				short(asString(item["hash"]), 12),
				asString(item["type"]),
				asString(item["role"]),
				asString(item["recorded_at"]),
				asString(item["message"]),
			)
		}
		return nil
	case "verify":
		report := asMap(payload)
		fmt.Fprintf(w, "ok  objects=%v  refs=%v\n", report["objects"], report["refs"])
		return nil
	case "init":
		created := asMap(payload)
		fmt.Fprintf(w, "created %s\n", asString(created["path"]))
		fmt.Fprintf(w, "main %s\n", asString(created["main"]))
		return nil
	case "demo":
		demo := asMap(payload)
		fmt.Fprintf(w, "created %s\n", asString(demo["path"]))
		fmt.Fprintf(w, "patient %s\n", asString(demo["patient"]))
		report := asMap(demo["verify"])
		fmt.Fprintf(w, "ok  objects=%v  refs=%v\n", report["objects"], report["refs"])
		return nil
	case "status":
		status := asMap(payload)
		patient := asMap(status["patient"])
		fmt.Fprintf(w, "%s  [%s]\n", asString(patient["display"]), asString(patient["patient_ref"]))
		fmt.Fprintf(w, "main %s\n", asString(status["main"]))
		for _, item := range asSlice(status["episodes"]) {
			episode := asMap(item)
			fmt.Fprintf(w, "episode %s  %s  %s\n", asString(episode["id"]), asString(episode["state"]), short(asString(episode["tip"]), 12))
		}
		for _, item := range asSlice(status["pulls"]) {
			pull := asMap(item)
			fmt.Fprintf(w, "consult %s  %s  %s\n", asString(pull["id"]), asString(pull["state"]), asString(pull["title"]))
		}
		return nil
	}
	if object, ok := payload.(map[string]any); ok {
		if _, ok := object["hash"]; ok {
			kind := asString(object["type"])
			if kind == "" {
				kind = asString(object["kind"])
			}
			message := asString(object["message"])
			if message == "" {
				message = asString(object["title"])
			}
			fmt.Fprintf(w, "%s  %s  %s\n", short(asString(object["hash"]), 12), kind, message)
			if cmd == "episode" {
				if id := asString(object["episode_id"]); id != "" {
					fmt.Fprintf(w, "episode %s\n", id)
				}
			}
			if cmd == "consult" && kind == "pull" {
				if id := asString(object["id"]); id != "" {
					fmt.Fprintf(w, "consult %s\n", id)
				}
			}
			return nil
		}
	}
	raw, err := canonical.MarshalIndent(payload)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(raw))
	return nil
}

func splitGlobals(argv []string) (string, bool, []string, bool, error) {
	var rest []string
	asJSON := false
	explicit := false
	path := "."
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--":
			rest = append(rest, argv[i+1:]...)
			return path, asJSON, rest, explicit, nil
		case arg == "-C" || arg == "--repo":
			i++
			if i >= len(argv) {
				return "", false, nil, false, mederr.Errorf("-C requires a path")
			}
			path = argv[i]
			explicit = true
		case strings.HasPrefix(arg, "--repo="):
			path = strings.TrimPrefix(arg, "--repo=")
			explicit = true
		case arg == "--json":
			asJSON = true
		default:
			rest = append(rest, arg)
		}
	}
	return path, asJSON, rest, explicit, nil
}

func actorFlags(fs *flag.FlagSet) (*string, *string, *string) {
	return fs.String("actor-id", "", "actor id"),
		fs.String("actor-display", "", "actor display name"),
		fs.String("actor-role", "", "resident, attending, system, or auditor")
}

func requireActor(id, display, role string) (repo.Actor, error) {
	if id == "" || display == "" || role == "" {
		return repo.Actor{}, errUsage{mederr.Errorf("--actor-id, --actor-display, and --actor-role are required")}
	}
	return repo.NewActor(id, display, role)
}

func requireOK(report map[string]any) error {
	ok, _ := report["ok"].(bool)
	if ok {
		return nil
	}
	var messages []string
	switch errors := report["errors"].(type) {
	case []string:
		messages = errors
	case []any:
		for _, item := range errors {
			messages = append(messages, fmt.Sprint(item))
		}
	}
	if len(messages) == 0 {
		messages = []string{"verify failed"}
	}
	return mederr.Integrityf("%s", strings.Join(messages, "; "))
}

func newFlag(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseArgs accepts flags before or after positional arguments.
// The standard flag parser stops at the first positional, which hides every
// flag written after an id in the examples.
func parseArgs(fs *flag.FlagSet, args []string) error {
	var positionals []string
	rest := append([]string(nil), args...)
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return errUsage{err}
		}
		left := append([]string(nil), fs.Args()...)
		if len(left) == 0 {
			break
		}
		positionals = append(positionals, left[0])
		rest = left[1:]
	}
	if err := fs.Parse(positionals); err != nil {
		return errUsage{err}
	}
	return nil
}

type errUsage struct{ error }

func (e errUsage) Error() string {
	if e.error == nil {
		return "usage"
	}
	return e.error.Error()
}

func (e errUsage) Unwrap() error { return e.error }

func asRewrite(err error, target **mederr.Rewrite) bool {
	if usage, ok := err.(errUsage); ok {
		return false && usage.error == nil
	}
	var rewrite *mederr.Rewrite
	if ok := errorAs(err, &rewrite); ok {
		*target = rewrite
		return true
	}
	return false
}

func errorAs(err error, target **mederr.Rewrite) bool {
	for err != nil {
		if rewrite, ok := err.(*mederr.Rewrite); ok {
			*target = rewrite
			return true
		}
		unwrap, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrap.Unwrap()
	}
	return false
}

func short(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[:n]
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

const usageText = `medgit [-C path] [--json] <command>

  init [dir]      create a patient record
  episode open    start a visit
  commit          add a fact to an open visit
  note            add a progress note
  observe         add a numeric result
  qsofa           add a qSOFA screening result
  consult         open, review, refresh, or merge a request to accept a visit
  abandon         close a visit without accepting it
  log             show history
  status          show the patient, visits, and requests
  verify          check that every file still matches its hash
  export-fhir     write a FHIR view of the record
  demo            file the official HL7 FHIR R4 f001 example
  show            print one stored object
  serve           start the HTTP server
  mcp             start the MCP server
  amend           refused
  rebase          refused
  reset           refused
`
