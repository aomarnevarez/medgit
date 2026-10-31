package repo_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aomarnevarez/medgit/internal/fhir"
	"github.com/aomarnevarez/medgit/internal/mederr"
	"github.com/aomarnevarez/medgit/internal/repo"
)

func clockFrom(start string) func() string {
	current, err := time.Parse(time.RFC3339, start)
	if err != nil {
		panic(err)
	}
	return func() string {
		stamp := current.UTC().Format("2006-01-02T15:04:05Z")
		current = current.Add(time.Second)
		return stamp
	}
}

func actors(t *testing.T) (attending, resident, lab, other repo.Actor) {
	t.Helper()
	var err error
	attending, err = repo.NewActor("att-ramirez", "Elena Ramirez, MD", "attending")
	if err != nil {
		t.Fatal(err)
	}
	resident, err = repo.NewActor("res-ortega", "Luis Ortega, MD", "resident")
	if err != nil {
		t.Fatal(err)
	}
	lab, err = repo.NewActor("bot-lis", "LIS", "system")
	if err != nil {
		t.Fatal(err)
	}
	other, err = repo.NewActor("att-salas", "Mateo Salas, MD", "attending")
	if err != nil {
		t.Fatal(err)
	}
	return attending, resident, lab, other
}

func initRepo(t *testing.T, clock func() string) *repo.MedRepo {
	t.Helper()
	attending, _, _, _ := actors(t)
	record, err := repo.Init(t.TempDir(), attending, "syn-pat-001", "Synthetic Patient One", "female", "1978-04-12", clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestLongitudinalRecordAndRoleRules(t *testing.T) {
	attending, resident, lab, _ := actors(t)
	record := initRepo(t, clockFrom("2026-04-02T14:00:00Z"))
	report, err := record.Verify()
	if err != nil || report["ok"] != true {
		t.Fatalf("verify = %#v %v", report, err)
	}
	if _, err := repo.Init(filepath.Join(t.TempDir(), "other"), resident, "x", "X", "", "", nil, nil); !errors.As(err, new(*mederr.Policy)) {
		t.Fatalf("resident init: %v", err)
	}
	episode, err := record.OpenEpisode(resident, "Emergency department admission", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	episodeID := episode["episode_id"].(string)
	if _, err := record.CommitNote(lab, episodeID, "no", ""); !errors.As(err, new(*mederr.Policy)) {
		t.Fatalf("system note: %v", err)
	}
	note, err := record.CommitNote(resident, episodeID, "Abdominal pain and dyspnea.", "")
	if err != nil {
		t.Fatal(err)
	}
	if note["author"].(map[string]any)["role"] != "resident" {
		t.Fatalf("author = %#v", note["author"])
	}
	vitals, err := record.CommitObservation(resident, episodeID, "8480-6", "Systolic blood pressure", "88", "mmHg", "mm[Hg]", "", "2026-04-02T14:10:00Z", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := record.CommitObservation(lab, episodeID, "2524-5", "Lactate", "4.2", "mmol/L", "mmol/L", "", "2026-04-02T14:20:00Z", "result"); err != nil {
		t.Fatal(err)
	}
	inference, err := record.CommitQSOFA(lab, episodeID, 24, 88, true, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(inference["message"].(string), "qSOFA 3/3") {
		t.Fatalf("message = %v", inference["message"])
	}
	consult, err := record.OpenPull(resident, episodeID, "High qSOFA — immediate review", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := record.Review(resident, consult["id"].(string), "approve", ""); !errors.As(err, new(*mederr.Policy)) {
		t.Fatalf("resident approve: %v", err)
	}
	if _, err := record.Merge(resident, consult["id"].(string), false); !errors.As(err, new(*mederr.Policy)) {
		t.Fatalf("resident merge: %v", err)
	}
	approved, err := record.Review(attending, consult["id"].(string), "approve", "Agreed, integrate.")
	if err != nil {
		t.Fatal(err)
	}
	if approved["state"] != "approved" {
		t.Fatalf("state = %v", approved["state"])
	}
	merged, err := record.Merge(attending, consult["id"].(string), false)
	if err != nil {
		t.Fatal(err)
	}
	if merged["type"] != "merge" || len(merged["parents"].([]any)) != 2 {
		t.Fatalf("merge = %#v", merged)
	}
	state, err := record.EpisodeState(episodeID)
	if err != nil || state != "merged" {
		t.Fatalf("state = %s %v", state, err)
	}
	bundle, err := record.ExportFHIR("")
	if err != nil {
		t.Fatal(err)
	}
	types := resourceTypes(bundle)
	for _, want := range []string{"Patient", "Observation", "RiskAssessment", "Provenance"} {
		if !types[want] {
			t.Fatalf("missing %s in %v", want, types)
		}
	}
	encounter := resourceByType(bundle, "Encounter")
	if encounter["status"] != "finished" {
		t.Fatalf("encounter status = %v", encounter["status"])
	}
	lactate := observationByCode(bundle, "2524-5")
	if lactate["valueQuantity"].(map[string]any)["value"] != 4.2 {
		t.Fatalf("lactate = %#v", lactate["valueQuantity"])
	}
	stored := vitals["resources"].([]any)[0].(map[string]any)["valueQuantity"].(map[string]any)["value"]
	if stored != "88" {
		t.Fatalf("stored vital = %#v", stored)
	}
	report, err = record.Verify()
	if err != nil || report["ok"] != true {
		t.Fatalf("verify after merge = %#v %v", report, err)
	}
	if _, err := record.CommitNote(resident, episodeID, "too late", ""); !errors.As(err, new(*mederr.Policy)) {
		t.Fatalf("commit after merge: %v", err)
	}
}

func TestRewriteOperationsDoNotExist(t *testing.T) {
	record := initRepo(t, nil)
	for _, err := range []error{record.Amend(), record.Rebase(), record.Reset()} {
		if !errors.As(err, new(*mederr.Rewrite)) {
			t.Fatalf("got %v", err)
		}
	}
}

func TestAuthorCannotApproveTheirOwnConsult(t *testing.T) {
	attending, _, _, other := actors(t)
	record := initRepo(t, clockFrom("2026-04-02T14:00:00Z"))
	episode, err := record.OpenEpisode(attending, "Follow-up", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	episodeID := episode["episode_id"].(string)
	if _, err := record.CommitNote(attending, episodeID, "No alarm features.", ""); err != nil {
		t.Fatal(err)
	}
	consult, err := record.OpenPull(attending, episodeID, "Close", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := record.Review(attending, consult["id"].(string), "approve", ""); !errors.As(err, new(*mederr.Policy)) {
		t.Fatalf("self approve: %v", err)
	}
	if _, err := record.Review(other, consult["id"].(string), "approve", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := record.Merge(attending, consult["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	state, err := record.EpisodeState(episodeID)
	if err != nil || state != "merged" {
		t.Fatalf("state = %s %v", state, err)
	}
}

func TestNewCommitDropsAStaleApproval(t *testing.T) {
	attending, resident, _, _ := actors(t)
	record := initRepo(t, clockFrom("2026-04-02T14:00:00Z"))
	episode, err := record.OpenEpisode(resident, "Observation", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	episodeID := episode["episode_id"].(string)
	if _, err := record.CommitNote(resident, episodeID, "Start.", ""); err != nil {
		t.Fatal(err)
	}
	consult, err := record.OpenPull(resident, episodeID, "Close", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := record.Review(attending, consult["id"].(string), "approve", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := record.CommitNote(resident, episodeID, "Late note.", ""); err != nil {
		t.Fatal(err)
	}
	refreshed, err := record.Pull(consult["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if refreshed["state"] != "open" || len(refreshed["reviews"].([]any)) != 0 {
		t.Fatalf("pull = %#v", refreshed["reviews"])
	}
	if _, err := record.Merge(attending, consult["id"].(string), false); !errors.As(err, new(*mederr.Policy)) {
		t.Fatalf("stale merge: %v", err)
	}
}

func TestDiscordantObservationsMustBeAcknowledged(t *testing.T) {
	attending, resident, _, _ := actors(t)
	record := initRepo(t, clockFrom("2026-04-02T14:00:00Z"))
	first, err := record.OpenEpisode(resident, "First contact", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := record.CommitObservation(resident, first["episode_id"].(string), "8480-6", "Systolic blood pressure", "120", "mmHg", "mm[Hg]", "", "2026-04-02T15:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	consult, err := record.OpenPull(resident, first["episode_id"].(string), "Integrate the first episode", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := record.Review(attending, consult["id"].(string), "approve", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := record.Merge(attending, consult["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	second, err := record.OpenEpisode(resident, "Reassessment", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := record.CommitObservation(resident, second["episode_id"].(string), "8480-6", "Systolic blood pressure", "80", "mmHg", "mm[Hg]", "", "2026-04-02T16:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	again, err := record.OpenPull(resident, second["episode_id"].(string), "Integrate the second episode", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(again["conflicts"].([]any)) != 1 {
		t.Fatalf("conflicts = %#v", again["conflicts"])
	}
	if _, err := record.Review(attending, again["id"].(string), "approve", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := record.Merge(attending, again["id"].(string), false); !errors.As(err, new(*mederr.Policy)) {
		t.Fatalf("unacknowledged merge: %v", err)
	}
	merged, err := record.Merge(attending, again["id"].(string), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged["notices"].([]any)) != 1 {
		t.Fatalf("notices = %#v", merged["notices"])
	}
	bundle, err := record.ExportFHIR("")
	if err != nil {
		t.Fatal(err)
	}
	values := map[float64]bool{}
	for _, entry := range bundle["entry"].([]any) {
		resource := entry.(map[string]any)["resource"].(map[string]any)
		if resource["resourceType"] != "Observation" {
			continue
		}
		values[asFloat(resource["valueQuantity"].(map[string]any)["value"])] = true
	}
	if !values[120] || !values[80] {
		t.Fatalf("values = %#v", values)
	}
}

func TestCorrectionKeepsTheOriginalAndMarksIt(t *testing.T) {
	_, resident, lab, _ := actors(t)
	record := initRepo(t, clockFrom("2026-04-02T14:00:00Z"))
	episode, err := record.OpenEpisode(resident, "Laboratory", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	episodeID := episode["episode_id"].(string)
	wrongResource, err := fhir.QuantityObservation("lac-1", "syn-pat-001", episodeID, "2524-5", "Lactate", "2.1", "mmol/L", "mmol/L", "2026-04-02T14:30:00Z")
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := record.Commit(lab, episodeID, "result", "Lactate 2.1", []any{wrongResource}, "normal", "", "2026-04-02T14:30:00Z")
	if err != nil {
		t.Fatal(err)
	}
	fixed, err := fhir.QuantityObservation("lac-1-fix", "syn-pat-001", episodeID, "2524-5", "Lactate", "4.2", "mmol/L", "mmol/L", "2026-04-02T14:30:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := record.Commit(lab, episodeID, "correction", "Lactate corrected to 4.2", []any{fixed}, "normal", wrong["hash"].(string), "2026-04-02T14:40:00Z"); err != nil {
		t.Fatal(err)
	}
	original, err := record.Object(wrong["hash"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if original["resources"].([]any)[0].(map[string]any)["valueQuantity"].(map[string]any)["value"] != "2.1" {
		t.Fatalf("original = %#v", original["resources"])
	}
	bundle, err := record.ExportFHIR("episodes/" + episodeID)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, entry := range bundle["entry"].([]any) {
		resource := entry.(map[string]any)["resource"].(map[string]any)
		if resource["resourceType"] == "Observation" {
			statuses[resource["id"].(string)] = resource["status"].(string)
		}
	}
	if statuses["lac-1"] != "entered-in-error" || statuses["lac-1-fix"] != "final" {
		t.Fatalf("statuses = %#v", statuses)
	}
}

func TestTamperAndBackwardsClockAreDetected(t *testing.T) {
	_, resident, _, _ := actors(t)
	dir := t.TempDir()
	attending, _, _, _ := actors(t)
	record, err := repo.Init(dir, attending, "syn-pat-001", "Synthetic Patient One", "female", "1978-04-12", clockFrom("2026-04-02T14:00:00Z"), nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := record.Verify()
	if err != nil || report["ok"] != true {
		t.Fatal(report, err)
	}
	objectID := reportMain(t, record)
	path := filepath.Join(dir, "objects", objectID[:2], objectID[2:])
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)-2] = blob[len(blob)-2] + 1
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	broken, err := record.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if broken["ok"] != false {
		t.Fatalf("tamper verify = %#v", broken)
	}

	record = initRepo(t, clockFrom("2026-04-02T14:00:00Z"))
	episode, err := record.OpenEpisode(resident, "Time", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	record.Clock = clockFrom("2026-04-02T13:00:00Z")
	if _, err := record.CommitNote(resident, episode["episode_id"].(string), "backwards", ""); !errors.As(err, new(*mederr.Integrity)) {
		t.Fatalf("backwards clock: %v", err)
	}
}

func TestSameDocumentIsTheSameObject(t *testing.T) {
	dir := t.TempDir()
	attending, _, _, _ := actors(t)
	record, err := repo.Init(dir, attending, "syn-pat-001", "Synthetic Patient One", "female", "1978-04-12", func() string { return "2026-04-02T14:00:00Z" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	objectID := reportMain(t, record)
	raw, err := os.ReadFile(filepath.Join(dir, "objects", objectID[:2], objectID[2:]))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != objectID {
		t.Fatalf("file hash %s != %s", hex.EncodeToString(sum[:]), objectID)
	}
	const canonicalHash = "7c9834376e3668a54393653444954e6106496d714a97d4eda8771a2f8108291c"
	if objectID != canonicalHash {
		t.Fatalf("object id %s does not match the canonical hash", objectID)
	}
}

func reportMain(t *testing.T, record *repo.MedRepo) string {
	t.Helper()
	status, err := record.Status()
	if err != nil {
		t.Fatal(err)
	}
	return status["main"].(string)
}

func resourceTypes(bundle map[string]any) map[string]bool {
	found := map[string]bool{}
	for _, entry := range bundle["entry"].([]any) {
		resource := entry.(map[string]any)["resource"].(map[string]any)
		found[resource["resourceType"].(string)] = true
	}
	return found
}

func resourceByType(bundle map[string]any, resourceType string) map[string]any {
	for _, entry := range bundle["entry"].([]any) {
		resource := entry.(map[string]any)["resource"].(map[string]any)
		if resource["resourceType"] == resourceType {
			return resource
		}
	}
	return nil
}

func observationByCode(bundle map[string]any, code string) map[string]any {
	for _, entry := range bundle["entry"].([]any) {
		resource := entry.(map[string]any)["resource"].(map[string]any)
		if resource["resourceType"] != "Observation" {
			continue
		}
		coding := resource["code"].(map[string]any)["coding"].([]any)
		if coding[0].(map[string]any)["code"] == code {
			return resource
		}
	}
	return nil
}

func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

func contains(text, part string) bool {
	return len(text) >= len(part) && (func() bool {
		for i := 0; i+len(part) <= len(text); i++ {
			if text[i:i+len(part)] == part {
				return true
			}
		}
		return false
	})()
}
