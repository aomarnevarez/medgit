// Package repo is the append-only clinical object log.
//
// A patient is a repository. An episode is a branch. A clinical fact is a
// commit. Refs only move forward, corrections are new objects, and a merge
// keeps every observation.
package repo

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aomarnevarez/medgit/internal/canonical"
	"github.com/aomarnevarez/medgit/internal/fhir"
	"github.com/aomarnevarez/medgit/internal/mederr"
	"github.com/aomarnevarez/medgit/internal/rules"
	"github.com/aomarnevarez/medgit/internal/store"
)

// Version is the object-format version stored on every object.
const Version = "0.2.0"

const conflictWindow = 6 * time.Hour

var (
	roles = map[string]struct{}{
		"resident": {}, "attending": {}, "system": {}, "auditor": {},
	}
	clinicalTypes = map[string]struct{}{
		"note": {}, "order": {}, "result": {}, "observation": {},
		"medication": {}, "procedure": {}, "feature": {},
	}
	systemTypes = map[string]struct{}{
		"result": {}, "feature": {}, "inference": {},
	}
	commitTypes = map[string]struct{}{
		"note": {}, "order": {}, "result": {}, "observation": {},
		"medication": {}, "procedure": {}, "feature": {},
		"episode-open": {}, "episode-abandon": {}, "correction": {},
		"inference": {}, "merge": {}, "patient-init": {},
	}
	openPR = map[string]struct{}{
		"open": {}, "approved": {}, "changes-requested": {},
	}
)

// Actor is the person or system the caller claims to be. MedGit does not authenticate it.
type Actor struct {
	ID      string
	Display string
	Role    string
}

// NewActor checks that the role is one this version understands.
func NewActor(id, display, role string) (Actor, error) {
	if _, ok := roles[role]; !ok {
		return Actor{}, mederr.Policyf("unknown role: %s", role)
	}
	if id == "" || display == "" {
		return Actor{}, mederr.Policyf("actor id and display are required")
	}
	return Actor{ID: id, Display: display, Role: role}, nil
}

// Map is the author object stored on a commit.
func (a Actor) Map() map[string]any {
	return map[string]any{"id": a.ID, "display": a.Display, "role": a.Role}
}

// MedRepo is one patient record on disk.
type MedRepo struct {
	store *store.Store
	// Clock returns the record time as a UTC Z timestamp. Tests replace it.
	// Production uses UTCNow.
	Clock func() string
}

// UTCNow formats the current UTC time the way the record stores it.
func UTCNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

// Open opens an existing record directory. The directory is not required to
// exist until the first read or write.
func Open(root string) *MedRepo {
	return &MedRepo{store: store.New(root)}
}

// Root is the record directory.
func (r *MedRepo) Root() string { return r.store.Root }

// Init creates a longitudinal record rooted at a patient-init commit.
// A nil resources slice stores a synthetic Patient. A non-nil slice is stored as given.
func Init(root string, actor Actor, patientRef, display, gender, birthDate string, clock func() string, resources []any) (*MedRepo, error) {
	if actor.Role != "attending" && actor.Role != "system" {
		return nil, mederr.Policyf("only an attending or a provisioning system can init a record")
	}
	if _, err := os.Stat(filepath.Join(root, "medgit.json")); err == nil {
		return nil, mederr.Policyf("record already exists at %s", root)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	if gender == "" {
		gender = "unknown"
	}
	if birthDate == "" {
		birthDate = "1978-04-12"
	}
	config := map[string]any{
		"medgit":      Version,
		"patient_ref": patientRef,
		"display":     display,
		"gender":      gender,
		"birth_date":  birthDate,
	}
	raw, err := canonical.MarshalIndent(config)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(root, "medgit.json"), append(raw, '\n'), 0o644); err != nil {
		return nil, err
	}
	repo := Open(root)
	repo.Clock = clock
	if resources == nil {
		resources = []any{fhir.PatientResource(patientRef, display, gender, birthDate)}
	}
	document, err := repo.commitDocument(
		actor,
		"patient-init",
		fmt.Sprintf("Open longitudinal record for %s", display),
		nil,
		"",
		resources,
		"normal",
		"",
		nil,
		"",
	)
	if err != nil {
		return nil, err
	}
	digest, err := repo.store.Put(document)
	if err != nil {
		return nil, err
	}
	if err := repo.store.WriteRef("main", digest); err != nil {
		return nil, err
	}
	return repo, nil
}

// Config reads medgit.json.
func (r *MedRepo) Config() (map[string]any, error) {
	raw, err := os.ReadFile(filepath.Join(r.store.Root, "medgit.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, mederr.Integrityf("medgit.json is missing")
		}
		return nil, err
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, mederr.Integrityf("medgit.json is not JSON")
	}
	return config, nil
}

// OpenEpisode forks an episode branch from main.
// A nil resources slice stores a synthetic Encounter.
func (r *MedRepo) OpenEpisode(actor Actor, reason, encounterClass, episodeID string, resources []any) (map[string]any, error) {
	if err := requireHuman(actor, "open an episode"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		return nil, mederr.Policyf("an episode needs a clinical reason")
	}
	main, err := r.requireRef("main")
	if err != nil {
		return nil, err
	}
	if episodeID == "" {
		episodeID = newID("ep_")
	}
	if existing, err := r.store.ReadRef("episodes/" + episodeID); err != nil {
		return nil, err
	} else if existing != "" {
		return nil, mederr.Policyf("episode %s already exists", episodeID)
	}
	config, err := r.Config()
	if err != nil {
		return nil, err
	}
	if encounterClass == "" {
		encounterClass = "EMER"
	}
	when := r.now()
	if resources == nil {
		resources = []any{fhir.EncounterResource(episodeID, asString(config["patient_ref"]), reason, encounterClass, when)}
	}
	document, err := r.commitDocument(actor, "episode-open", reason, []string{main}, episodeID, resources, "normal", "", nil, when)
	if err != nil {
		return nil, err
	}
	digest, err := r.store.Put(document)
	if err != nil {
		return nil, err
	}
	if err := r.store.WriteRef("episodes/"+episodeID, digest); err != nil {
		return nil, err
	}
	return withHash(digest, document), nil
}

// Commit appends a fact to an open episode.
func (r *MedRepo) Commit(actor Actor, episodeID, commitType, message string, resources []any, sensitivity, corrects, authoredAt string) (map[string]any, error) {
	if err := requireWriter(actor); err != nil {
		return nil, err
	}
	if _, ok := clinicalTypes[commitType]; !ok && commitType != "correction" && commitType != "inference" {
		return nil, mederr.Policyf("%s is not committed through commit()", commitType)
	}
	if sensitivity == "" {
		sensitivity = "normal"
	}
	if sensitivity != "normal" && sensitivity != "restricted" {
		return nil, mederr.Policyf("unknown sensitivity: %s", sensitivity)
	}
	if strings.TrimSpace(message) == "" {
		return nil, mederr.Policyf("a commit needs a message")
	}
	if len(resources) == 0 {
		return nil, mederr.Policyf("a clinical commit needs at least one resource")
	}
	state, err := r.EpisodeState(episodeID)
	if err != nil {
		return nil, err
	}
	if state != "open" && state != "in-review" {
		return nil, mederr.Policyf("episode %s is %s; commit on an open episode", episodeID, state)
	}
	var original map[string]any
	if commitType == "correction" {
		if corrects == "" {
			return nil, mederr.Policyf("a correction must name the commit it corrects")
		}
		original, err = r.Object(corrects)
		if err != nil {
			return nil, err
		}
		if asString(original["kind"]) != "commit" {
			return nil, mederr.Policyf("corrections point at commits")
		}
		if asString(original["episode_id"]) != episodeID {
			return nil, mederr.Policyf("corrections stay on the episode of the original fact")
		}
	} else if corrects != "" {
		return nil, mederr.Policyf("only a correction commit may set corrects")
	}
	if actor.Role == "system" {
		if commitType == "correction" {
			if asString(asMap(original["author"])["role"]) != "system" {
				return nil, mederr.Policyf("a system may only correct its own commits")
			}
		} else if _, ok := systemTypes[commitType]; !ok {
			return nil, mederr.Policyf("a system actor cannot commit %s", commitType)
		}
	}
	if actor.Role == "resident" && commitType == "inference" {
		return nil, mederr.Policyf("inference commits are produced by a system or an attending")
	}
	parent, err := r.requireRef("episodes/" + episodeID)
	if err != nil {
		return nil, err
	}
	document, err := r.commitDocument(actor, commitType, message, []string{parent}, episodeID, resources, sensitivity, corrects, nil, authoredAt)
	if err != nil {
		return nil, err
	}
	digest, err := r.store.Put(document)
	if err != nil {
		return nil, err
	}
	if err := r.store.WriteRef("episodes/"+episodeID, digest); err != nil {
		return nil, err
	}
	if err := r.invalidateReviews(episodeID, digest); err != nil {
		return nil, err
	}
	return withHash(digest, document), nil
}

// CommitNote appends a progress note.
func (r *MedRepo) CommitNote(actor Actor, episodeID, text, authoredAt string) (map[string]any, error) {
	when := authoredAt
	if when == "" {
		when = r.now()
	}
	config, err := r.Config()
	if err != nil {
		return nil, err
	}
	resource := fhir.DocumentReference(newID("note_"), asString(config["patient_ref"]), episodeID, text, actor.Display, when)
	message := text
	runes := []rune(message)
	if len(runes) > 120 {
		message = string(runes[:120])
	}
	return r.Commit(actor, episodeID, "note", message, []any{resource}, "normal", "", when)
}

// CommitObservation appends a quantity observation or a lab result.
func (r *MedRepo) CommitObservation(actor Actor, episodeID, loinc, display, value, unit, ucum, message, authoredAt, commitType string) (map[string]any, error) {
	if commitType == "" {
		commitType = "observation"
	}
	if commitType != "observation" && commitType != "result" {
		return nil, mederr.Policyf("quantity helpers only write observation or result commits")
	}
	when := authoredAt
	if when == "" {
		when = r.now()
	}
	config, err := r.Config()
	if err != nil {
		return nil, err
	}
	resource, err := fhir.QuantityObservation(newID("obs_"), asString(config["patient_ref"]), episodeID, loinc, display, value, unit, ucum, when)
	if err != nil {
		return nil, err
	}
	if message == "" {
		message = fmt.Sprintf("%s %s %s", display, value, unit)
	}
	return r.Commit(actor, episodeID, commitType, message, []any{resource}, "normal", "", when)
}

// CommitQSOFA appends an inference commit for the reference qSOFA rule.
func (r *MedRepo) CommitQSOFA(actor Actor, episodeID string, respiratoryRate, systolicBP int, alteredMentation bool, message, authoredAt string) (map[string]any, error) {
	if actor.Role != "system" && actor.Role != "attending" {
		return nil, mederr.Policyf("only a system or an attending can commit a rule result")
	}
	scored, err := rules.Score(respiratoryRate, systolicBP, alteredMentation)
	if err != nil {
		return nil, err
	}
	when := authoredAt
	if when == "" {
		when = r.now()
	}
	config, err := r.Config()
	if err != nil {
		return nil, err
	}
	resource := fhir.RiskAssessment(newID("risk_"), asString(config["patient_ref"]), episodeID, scored.Score, scored.Band, scored.Narrative(), when)
	if message == "" {
		message = scored.Narrative()
	}
	return r.Commit(actor, episodeID, "inference", message, []any{resource}, "normal", "", when)
}

// OpenPull opens a consult against the current episode tip and main.
func (r *MedRepo) OpenPull(actor Actor, episodeID, title, body string) (map[string]any, error) {
	if err := requireHuman(actor, "open a consult"); err != nil {
		return nil, err
	}
	state, err := r.EpisodeState(episodeID)
	if err != nil {
		return nil, err
	}
	if state == "merged" {
		return nil, mederr.Policyf("episode is already on the longitudinal record")
	}
	if state == "abandoned" {
		return nil, mederr.Policyf("episode was abandoned")
	}
	if strings.TrimSpace(title) == "" {
		return nil, mederr.Policyf("a consult needs a title")
	}
	existingPulls, err := r.Pulls()
	if err != nil {
		return nil, err
	}
	for _, existing := range existingPulls {
		if asString(existing["episode_id"]) == episodeID {
			if _, open := openPR[asString(existing["state"])]; open {
				return nil, mederr.Policyf("consult %s is already open for this episode", asString(existing["id"]))
			}
		}
	}
	source, err := r.requireRef("episodes/" + episodeID)
	if err != nil {
		return nil, err
	}
	target, err := r.requireRef("main")
	if err != nil {
		return nil, err
	}
	conflicts, err := r.SemanticConflicts(episodeID)
	if err != nil {
		return nil, err
	}
	identifier := newID("pr_")
	created := r.now()
	updated := r.now()
	document := map[string]any{
		"medgit":       Version,
		"kind":         "pull",
		"id":           identifier,
		"episode_id":   episodeID,
		"title":        title,
		"body":         body,
		"author":       actor.Map(),
		"source_tip":   source,
		"target_tip":   target,
		"state":        "open",
		"reviews":      []any{},
		"conflicts":    mapsToAny(conflicts),
		"previous":     nil,
		"merge_commit": nil,
		"created_at":   created,
		"updated_at":   updated,
	}
	digest, err := r.store.Put(document)
	if err != nil {
		return nil, err
	}
	if err := r.store.WriteRef("pulls/"+identifier, digest); err != nil {
		return nil, err
	}
	return withHash(digest, document), nil
}

// Review records a comment, an approval, or a request for changes.
func (r *MedRepo) Review(actor Actor, prID, decision, comment string) (map[string]any, error) {
	if decision != "approve" && decision != "request-changes" && decision != "comment" {
		return nil, mederr.Policyf("unknown decision: %s", decision)
	}
	if actor.Role != "resident" && actor.Role != "attending" {
		return nil, mederr.Policyf("only a clinician can review a consult")
	}
	if decision == "approve" && actor.Role != "attending" {
		return nil, mederr.Policyf("only an attending can approve a consult")
	}
	current, err := r.Pull(prID)
	if err != nil {
		return nil, err
	}
	if _, open := openPR[asString(current["state"])]; !open {
		return nil, mederr.Policyf("consult is %s", asString(current["state"]))
	}
	if decision == "approve" && actor.ID == asString(asMap(current["author"])["id"]) {
		return nil, mederr.Policyf("you cannot approve your own consult")
	}
	reviews := append([]any{}, asSlice(current["reviews"])...)
	reviews = append(reviews, map[string]any{
		"actor":    actor.Map(),
		"decision": decision,
		"comment":  comment,
		"at":       r.now(),
	})
	state := asString(current["state"])
	if decision == "approve" {
		state = "approved"
	} else if decision == "request-changes" {
		state = "changes-requested"
	}
	return r.advancePull(current, pullPatch{State: state, Reviews: reviews})
}

// RefreshPull retargets a consult at the current tips and drops its reviews.
func (r *MedRepo) RefreshPull(actor Actor, prID string) (map[string]any, error) {
	if err := requireHuman(actor, "refresh a consult"); err != nil {
		return nil, err
	}
	current, err := r.Pull(prID)
	if err != nil {
		return nil, err
	}
	if asString(current["state"]) == "merged" {
		return nil, mederr.Policyf("consult is already merged")
	}
	episodeID := asString(current["episode_id"])
	source, err := r.requireRef("episodes/" + episodeID)
	if err != nil {
		return nil, err
	}
	target, err := r.requireRef("main")
	if err != nil {
		return nil, err
	}
	conflicts, err := r.SemanticConflicts(episodeID)
	if err != nil {
		return nil, err
	}
	return r.advancePull(current, pullPatch{
		State:        "open",
		Reviews:      []any{},
		SourceTip:    source,
		HasSource:    true,
		TargetTip:    target,
		HasTarget:    true,
		Conflicts:    mapsToAny(conflicts),
		HasConflicts: true,
	})
}

// Merge lands an approved consult on main. Discordant observations must be acknowledged.
func (r *MedRepo) Merge(actor Actor, prID string, acknowledgeConflicts bool) (map[string]any, error) {
	if actor.Role != "attending" {
		return nil, mederr.Policyf("only an attending can merge onto the longitudinal record")
	}
	current, err := r.Pull(prID)
	if err != nil {
		return nil, err
	}
	if asString(current["state"]) != "approved" {
		return nil, mederr.Policyf("merge requires an attending approval")
	}
	if !hasIndependentApproval(current) {
		return nil, mederr.Policyf("an attending other than the author must approve")
	}
	episodeID := asString(current["episode_id"])
	state, err := r.EpisodeState(episodeID)
	if err != nil {
		return nil, err
	}
	if state == "abandoned" {
		return nil, mederr.Policyf("refusing to merge an abandoned episode")
	}
	source, err := r.requireRef("episodes/" + episodeID)
	if err != nil {
		return nil, err
	}
	target, err := r.requireRef("main")
	if err != nil {
		return nil, err
	}
	if source != asString(current["source_tip"]) || target != asString(current["target_tip"]) {
		return nil, mederr.Policyf("the episode or the longitudinal record moved; refresh the consult")
	}
	conflicts, err := r.SemanticConflicts(episodeID)
	if err != nil {
		return nil, err
	}
	if len(conflicts) > 0 && !acknowledgeConflicts {
		return nil, mederr.Policyf("%d discordant observation(s); acknowledge them to merge", len(conflicts))
	}
	document, err := r.commitDocument(
		actor,
		"merge",
		fmt.Sprintf("Merge %s: %s", asString(current["id"]), asString(current["title"])),
		[]string{target, source},
		episodeID,
		nil,
		"normal",
		"",
		conflicts,
		"",
	)
	if err != nil {
		return nil, err
	}
	digest, err := r.store.Put(document)
	if err != nil {
		return nil, err
	}
	if err := r.store.WriteRef("main", digest); err != nil {
		return nil, err
	}
	if _, err := r.advancePull(current, pullPatch{
		State:       asStringKept(current["state"], "merged"),
		Reviews:     asSlice(current["reviews"]),
		MergeCommit: digest,
		HasMerge:    true,
	}); err != nil {
		return nil, err
	}
	return withHash(digest, document), nil
}

// Abandon closes an episode. Only an attending can do it, and the reason is kept.
func (r *MedRepo) Abandon(actor Actor, episodeID, reason string) (map[string]any, error) {
	if err := requireHuman(actor, "abandon an episode"); err != nil {
		return nil, err
	}
	if actor.Role != "attending" {
		return nil, mederr.Policyf("only an attending can abandon an episode")
	}
	state, err := r.EpisodeState(episodeID)
	if err != nil {
		return nil, err
	}
	if state != "open" && state != "in-review" {
		return nil, mederr.Policyf("episode is not open")
	}
	if strings.TrimSpace(reason) == "" {
		return nil, mederr.Policyf("abandoning an episode requires a reason")
	}
	parent, err := r.requireRef("episodes/" + episodeID)
	if err != nil {
		return nil, err
	}
	document, err := r.commitDocument(actor, "episode-abandon", reason, []string{parent}, episodeID, nil, "normal", "", nil, "")
	if err != nil {
		return nil, err
	}
	digest, err := r.store.Put(document)
	if err != nil {
		return nil, err
	}
	if err := r.store.WriteRef("episodes/"+episodeID, digest); err != nil {
		return nil, err
	}
	pulls, err := r.Pulls()
	if err != nil {
		return nil, err
	}
	for _, pull := range pulls {
		if asString(pull["episode_id"]) == episodeID {
			if _, open := openPR[asString(pull["state"])]; open {
				if _, err := r.advancePull(pull, pullPatch{State: "closed", Reviews: asSlice(pull["reviews"])}); err != nil {
					return nil, err
				}
			}
		}
	}
	return withHash(digest, document), nil
}

// Amend refuses. A correction commit is the way to record a wrong fact.
func (r *MedRepo) Amend() error {
	return &mederr.Rewrite{Msg: "amend rewrites a clinical fact. Commit a correction that references the original hash."}
}

// Rebase refuses. A merge is a union.
func (r *MedRepo) Rebase() error {
	return &mederr.Rewrite{Msg: "rebase rewrites history. Episodes merge by union; discordant values become review notices."}
}

// Reset refuses. Refs only advance.
func (r *MedRepo) Reset() error {
	return &mederr.Rewrite{Msg: "reset would move a ref backwards. Refs only advance by a new object."}
}

// Object loads one stored object. The returned map has no hash field.
func (r *MedRepo) Object(digest string) (map[string]any, error) {
	return r.store.Get(digest)
}

// EpisodeState derives open, in-review, merged, or abandoned from the log.
func (r *MedRepo) EpisodeState(episodeID string) (string, error) {
	tip, err := r.store.ReadRef("episodes/" + episodeID)
	if err != nil {
		return "", err
	}
	if tip == "" {
		return "", mederr.Policyf("unknown episode %s", episodeID)
	}
	episodeHistory, err := r.History(tip, false)
	if err != nil {
		return "", err
	}
	for _, commit := range episodeHistory {
		if asString(commit["type"]) == "episode-abandon" {
			return "abandoned", nil
		}
	}
	main, err := r.store.ReadRef("main")
	if err != nil {
		return "", err
	}
	if main != "" {
		mainHistory, err := r.History(main, true)
		if err != nil {
			return "", err
		}
		for _, commit := range mainHistory {
			if asString(commit["type"]) == "merge" && asString(commit["episode_id"]) == episodeID {
				return "merged", nil
			}
		}
	}
	pulls, err := r.Pulls()
	if err != nil {
		return "", err
	}
	for _, pull := range pulls {
		if asString(pull["episode_id"]) == episodeID {
			if _, open := openPR[asString(pull["state"])]; open {
				return "in-review", nil
			}
		}
	}
	return "open", nil
}

// History walks ancestors, newest first. allParents follows every parent;
// otherwise only the first parent is followed.
func (r *MedRepo) History(tip string, allParents bool) ([]map[string]any, error) {
	var order []map[string]any
	seen := map[string]struct{}{}
	var walk func(string) error
	walk = func(digest string) error {
		if _, ok := seen[digest]; ok {
			return nil
		}
		seen[digest] = struct{}{}
		document, err := r.store.Get(digest)
		if err != nil {
			return err
		}
		if asString(document["kind"]) != "commit" {
			return mederr.Integrityf("%s is not a commit", digest)
		}
		parents := asSlice(document["parents"])
		follow := parents
		if !allParents {
			if len(parents) > 0 {
				follow = parents[:1]
			} else {
				follow = nil
			}
		}
		for _, parent := range follow {
			if err := walk(asString(parent)); err != nil {
				return err
			}
		}
		order = append(order, withHash(digest, document))
		return nil
	}
	if err := walk(tip); err != nil {
		return nil, err
	}
	reverseMaps(order)
	return order, nil
}

// Log reads a ref. Anything other than main is an episode id or an episodes/ ref.
func (r *MedRepo) Log(ref string, allParents bool) ([]map[string]any, error) {
	if ref == "" {
		ref = "main"
	}
	if ref != "main" && !strings.HasPrefix(ref, "episodes/") {
		ref = "episodes/" + ref
	}
	tip, err := r.store.ReadRef(ref)
	if err != nil {
		return nil, err
	}
	if tip == "" {
		return nil, mederr.Policyf("unknown ref %s", ref)
	}
	return r.History(tip, allParents)
}

// Pulls returns the current pull object for every consult.
func (r *MedRepo) Pulls() ([]map[string]any, error) {
	names, err := r.store.ListRefs("pulls/")
	if err != nil {
		return nil, err
	}
	found := []map[string]any{}
	for _, name := range names {
		digest, err := r.store.ReadRef(name)
		if err != nil {
			return nil, err
		}
		if digest == "" {
			continue
		}
		document, err := r.store.Get(digest)
		if err != nil {
			return nil, err
		}
		found = append(found, withHash(digest, document))
	}
	return found, nil
}

// Pull loads the latest object for one consult.
func (r *MedRepo) Pull(prID string) (map[string]any, error) {
	digest, err := r.store.ReadRef("pulls/" + prID)
	if err != nil {
		return nil, err
	}
	if digest == "" {
		return nil, mederr.Policyf("unknown consult %s", prID)
	}
	document, err := r.store.Get(digest)
	if err != nil {
		return nil, err
	}
	return withHash(digest, document), nil
}

// SemanticConflicts reports observations of the same code that disagree inside six hours.
func (r *MedRepo) SemanticConflicts(episodeID string) ([]map[string]any, error) {
	episodeTip, err := r.requireRef("episodes/" + episodeID)
	if err != nil {
		return nil, err
	}
	mainTip, err := r.requireRef("main")
	if err != nil {
		return nil, err
	}
	episodeHistory, err := r.History(episodeTip, true)
	if err != nil {
		return nil, err
	}
	mainHistory, err := r.History(mainTip, true)
	if err != nil {
		return nil, err
	}
	var episodeCommits, mainCommits []map[string]any
	for _, commit := range episodeHistory {
		if asString(commit["episode_id"]) == episodeID {
			episodeCommits = append(episodeCommits, commit)
		}
	}
	for _, commit := range mainHistory {
		if asString(commit["episode_id"]) != episodeID {
			mainCommits = append(mainCommits, commit)
		}
	}
	episodeObs, err := observations(episodeCommits)
	if err != nil {
		return nil, err
	}
	mainObs, err := observations(mainCommits)
	if err != nil {
		return nil, err
	}
	notices := []map[string]any{}
	for _, left := range episodeObs {
		for _, right := range mainObs {
			if left.key != right.key || left.value == right.value {
				continue
			}
			delta := left.when.Sub(right.when)
			if delta < 0 {
				delta = -delta
			}
			if delta > conflictWindow {
				continue
			}
			notices = append(notices, map[string]any{
				"code":           left.key,
				"episode_value":  left.value,
				"main_value":     right.value,
				"episode_commit": left.commit,
				"main_commit":    right.commit,
				"severity":       "review",
			})
		}
	}
	return notices, nil
}

// Status summarizes the patient, episode tips, and consults.
func (r *MedRepo) Status() (map[string]any, error) {
	config, err := r.Config()
	if err != nil {
		return nil, err
	}
	names, err := r.store.ListRefs("episodes/")
	if err != nil {
		return nil, err
	}
	episodes := []any{}
	for _, name := range names {
		episodeID := strings.SplitN(name, "/", 2)[1]
		state, err := r.EpisodeState(episodeID)
		if err != nil {
			return nil, err
		}
		tip, err := r.store.ReadRef(name)
		if err != nil {
			return nil, err
		}
		episodes = append(episodes, map[string]any{
			"id":    episodeID,
			"state": state,
			"tip":   tip,
		})
	}
	main, err := r.store.ReadRef("main")
	if err != nil {
		return nil, err
	}
	var mainVal any
	if main != "" {
		mainVal = main
	}
	pulls, err := r.Pulls()
	if err != nil {
		return nil, err
	}
	brief := []any{}
	for _, item := range pulls {
		brief = append(brief, map[string]any{
			"id":         item["id"],
			"state":      item["state"],
			"title":      item["title"],
			"episode_id": item["episode_id"],
		})
	}
	return map[string]any{
		"patient":  config,
		"main":     mainVal,
		"episodes": episodes,
		"pulls":    brief,
	}, nil
}

// Verify rehashes every object and checks parent links, corrections, and pull tips.
func (r *MedRepo) Verify() (map[string]any, error) {
	errors := []string{}
	seen := map[string]struct{}{}
	ids, err := r.store.ObjectIDs()
	if err != nil {
		return nil, err
	}
	for _, digest := range ids {
		if _, err := r.store.Get(digest); err != nil {
			errors = append(errors, err.Error())
			continue
		}
		seen[digest] = struct{}{}
	}
	names, err := r.store.ListRefs("")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, name := range names {
		target, err := r.store.ReadRef(name)
		if err != nil {
			errors = append(errors, err.Error())
			continue
		}
		if target == "" {
			continue
		}
		refs[name] = target
		if _, ok := seen[target]; !ok {
			errors = append(errors, fmt.Sprintf("ref %s points at missing object %s", name, target))
		}
	}
	digests := make([]string, 0, len(seen))
	for digest := range seen {
		digests = append(digests, digest)
	}
	sortStrings(digests)
	for _, digest := range digests {
		document, err := r.store.Get(digest)
		if err != nil {
			continue
		}
		switch asString(document["kind"]) {
		case "commit":
			r.checkCommit(digest, document, seen, &errors)
		case "pull":
			r.checkPull(digest, document, seen, &errors)
		default:
			errors = append(errors, fmt.Sprintf("%s has unknown kind", digest))
		}
	}
	config, err := r.Config()
	if err != nil {
		errors = append(errors, err.Error())
		config = map[string]any{}
	}
	main, ok := refs["main"]
	if !ok {
		errors = append(errors, "main ref is missing")
	} else {
		history, err := r.History(main, true)
		if err != nil {
			errors = append(errors, err.Error())
		} else {
			foundPatient := false
			for _, commit := range history {
				if asString(commit["type"]) != "patient-init" {
					continue
				}
				foundPatient = true
				var resource map[string]any
				for _, item := range asSlice(commit["resources"]) {
					candidate, ok := item.(map[string]any)
					if ok && asString(candidate["resourceType"]) == "Patient" {
						resource = candidate
						break
					}
				}
				if resource == nil || asString(resource["id"]) != asString(config["patient_ref"]) {
					errors = append(errors, "patient-init does not match medgit.json")
				}
			}
			if !foundPatient {
				errors = append(errors, "main does not reach a patient-init commit")
			}
		}
	}
	return map[string]any{
		"ok":      len(errors) == 0,
		"errors":  errors,
		"objects": len(seen),
		"refs":    len(refs),
	}, nil
}

// ExportFHIR projects ref into a FHIR collection Bundle. The bundle is not hashed.
func (r *MedRepo) ExportFHIR(ref string) (map[string]any, error) {
	if ref == "" {
		ref = "main"
	}
	tip, err := r.requireRef(ref)
	if err != nil {
		return nil, err
	}
	history, err := r.History(tip, true)
	if err != nil {
		return nil, err
	}
	commits := make([]map[string]any, len(history))
	for i := range history {
		commits[i] = history[len(history)-1-i]
	}
	corrected := map[string]string{}
	merged := map[string]map[string]any{}
	for _, commit := range commits {
		if asString(commit["type"]) == "correction" && asString(commit["corrects"]) != "" {
			corrected[asString(commit["corrects"])] = asString(commit["hash"])
		}
		if asString(commit["type"]) == "merge" && asString(commit["episode_id"]) != "" {
			merged[asString(commit["episode_id"])] = commit
		}
	}
	entries := []any{}
	seenURL := map[string]struct{}{}
	for _, commit := range commits {
		fix := corrected[asString(commit["hash"])]
		for _, item := range asSlice(commit["resources"]) {
			resource, ok := item.(map[string]any)
			if !ok {
				continue
			}
			projected := fhir.ProjectResource(resource, fix)
			if asString(projected["resourceType"]) == "Encounter" {
				if mergeCommit, ok := merged[asString(projected["id"])]; ok {
					projected["status"] = "finished"
					period := map[string]any{}
					if existing, ok := projected["period"].(map[string]any); ok {
						for key, value := range existing {
							period[key] = value
						}
					}
					period["end"] = mergeCommit["recorded_at"]
					projected["period"] = period
				}
			}
			fullURL := fmt.Sprintf("%s/%s", asString(projected["resourceType"]), asString(projected["id"]))
			if _, ok := seenURL[fullURL]; ok {
				continue
			}
			seenURL[fullURL] = struct{}{}
			entries = append(entries, map[string]any{"fullUrl": fullURL, "resource": projected})
		}
		var targets []string
		for _, item := range asSlice(commit["resources"]) {
			resource, ok := item.(map[string]any)
			if !ok {
				continue
			}
			resourceType := asString(resource["resourceType"])
			id := asString(resource["id"])
			if resourceType != "" && id != "" {
				targets = append(targets, resourceType+"/"+id)
			}
		}
		prov := fhir.Provenance(asString(commit["hash"]), commit, targets)
		entries = append(entries, map[string]any{
			"fullUrl":  "Provenance/" + asString(prov["id"]),
			"resource": prov,
		})
	}
	prefix := tip
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	return map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"id":           "bundle-" + prefix,
		"timestamp":    r.now(),
		"entry":        entries,
	}, nil
}

func (r *MedRepo) commitDocument(actor Actor, commitType, message string, parents []string, episodeID string, resources []any, sensitivity, corrects string, notices []map[string]any, authoredAt string) (map[string]any, error) {
	if _, ok := commitTypes[commitType]; !ok {
		return nil, mederr.Policyf("unknown commit type: %s", commitType)
	}
	recorded := r.now()
	recordedTime, err := ParseTime(recorded)
	if err != nil {
		return nil, err
	}
	authored := authoredAt
	if authored == "" {
		authored = recorded
	}
	if _, err := ParseTime(authored); err != nil {
		return nil, err
	}
	for _, parent := range parents {
		previous, err := r.store.Get(parent)
		if err != nil {
			return nil, err
		}
		previousTime, err := ParseTime(asString(previous["recorded_at"]))
		if err != nil {
			return nil, err
		}
		if recordedTime.Before(previousTime) {
			return nil, mederr.Integrityf("recorded_at moved backwards along the parent chain")
		}
	}
	if resources == nil {
		resources = []any{}
	}
	for _, resource := range resources {
		object, ok := resource.(map[string]any)
		if !ok {
			return nil, mederr.Policyf("resources must be FHIR-shaped objects")
		}
		if _, ok := object["resourceType"]; !ok {
			return nil, mederr.Policyf("resources must be FHIR-shaped objects")
		}
	}
	var correctsVal any
	if corrects != "" {
		correctsVal = corrects
	}
	var episodeVal any
	if episodeID != "" {
		episodeVal = episodeID
	}
	if notices == nil {
		notices = []map[string]any{}
	}
	if parents == nil {
		parents = []string{}
	}
	document := map[string]any{
		"authored_at": authored,
		"author":      actor.Map(),
		"corrects":    correctsVal,
		"episode_id":  episodeVal,
		"kind":        "commit",
		"medgit":      Version,
		"message":     message,
		"notices":     mapsToAny(notices),
		"parents":     stringsToAny(parents),
		"recorded_at": recorded,
		"resources":   resources,
		"sensitivity": sensitivity,
		"type":        commitType,
	}
	if _, err := canonical.Bytes(document); err != nil {
		return nil, mederr.Policyf("%s", err.Error())
	}
	return document, nil
}

type pullPatch struct {
	State        string
	Reviews      []any
	SourceTip    string
	HasSource    bool
	TargetTip    string
	HasTarget    bool
	Conflicts    []any
	HasConflicts bool
	MergeCommit  string
	HasMerge     bool
}

func (r *MedRepo) advancePull(current map[string]any, patch pullPatch) (map[string]any, error) {
	source := asString(current["source_tip"])
	if patch.HasSource {
		source = patch.SourceTip
	}
	target := asString(current["target_tip"])
	if patch.HasTarget {
		target = patch.TargetTip
	}
	var conflicts any
	if patch.HasConflicts {
		if patch.Conflicts == nil {
			conflicts = []any{}
		} else {
			conflicts = patch.Conflicts
		}
	} else {
		conflicts = current["conflicts"]
	}
	var merge any
	if patch.HasMerge {
		if patch.MergeCommit != "" {
			merge = patch.MergeCommit
		}
	} else {
		merge = current["merge_commit"]
	}
	reviews := patch.Reviews
	if reviews == nil {
		reviews = []any{}
	}
	previous := asString(current["hash"])
	document := map[string]any{
		"author":       current["author"],
		"body":         current["body"],
		"conflicts":    conflicts,
		"created_at":   current["created_at"],
		"episode_id":   current["episode_id"],
		"id":           current["id"],
		"kind":         "pull",
		"medgit":       Version,
		"merge_commit": merge,
		"previous":     previous,
		"reviews":      reviews,
		"source_tip":   source,
		"state":        patch.State,
		"target_tip":   target,
		"title":        current["title"],
		"updated_at":   r.now(),
	}
	digest, err := r.store.Put(document)
	if err != nil {
		return nil, err
	}
	if err := r.store.WriteRef("pulls/"+asString(current["id"]), digest); err != nil {
		return nil, err
	}
	return withHash(digest, document), nil
}

func (r *MedRepo) invalidateReviews(episodeID, newTip string) error {
	pulls, err := r.Pulls()
	if err != nil {
		return err
	}
	for _, pull := range pulls {
		if asString(pull["episode_id"]) != episodeID {
			continue
		}
		if _, open := openPR[asString(pull["state"])]; !open {
			continue
		}
		if asString(pull["source_tip"]) == newTip {
			continue
		}
		conflicts, err := r.SemanticConflicts(episodeID)
		if err != nil {
			return err
		}
		if _, err := r.advancePull(pull, pullPatch{
			State:        "open",
			Reviews:      []any{},
			SourceTip:    newTip,
			HasSource:    true,
			Conflicts:    mapsToAny(conflicts),
			HasConflicts: true,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *MedRepo) checkCommit(digest string, document map[string]any, seen map[string]struct{}, errors *[]string) {
	if _, ok := commitTypes[asString(document["type"])]; !ok {
		*errors = append(*errors, fmt.Sprintf("%s has unknown type", digest))
	}
	for _, parent := range asSlice(document["parents"]) {
		parentID := asString(parent)
		if _, ok := seen[parentID]; !ok {
			*errors = append(*errors, fmt.Sprintf("%s parent %s is missing", digest, parentID))
			continue
		}
		previous, err := r.store.Get(parentID)
		if err != nil {
			*errors = append(*errors, err.Error())
			continue
		}
		recorded, err := ParseTime(asString(document["recorded_at"]))
		if err != nil {
			*errors = append(*errors, err.Error())
			continue
		}
		previousTime, err := ParseTime(asString(previous["recorded_at"]))
		if err != nil {
			*errors = append(*errors, err.Error())
			continue
		}
		if recorded.Before(previousTime) {
			*errors = append(*errors, fmt.Sprintf("%s recorded_at is earlier than its parent", digest))
		}
	}
	corrects := document["corrects"]
	if asString(document["type"]) == "correction" && (corrects == nil || corrects == "") {
		*errors = append(*errors, fmt.Sprintf("%s correction has no target", digest))
	}
	if corrects != nil && corrects != "" {
		if _, ok := seen[asString(corrects)]; !ok {
			*errors = append(*errors, fmt.Sprintf("%s corrects missing object %s", digest, asString(corrects)))
		}
	}
	if asString(document["type"]) == "merge" && len(asSlice(document["parents"])) != 2 {
		*errors = append(*errors, fmt.Sprintf("%s merge must have two parents", digest))
	}
}

func (r *MedRepo) checkPull(digest string, document map[string]any, seen map[string]struct{}, errors *[]string) {
	if previous := document["previous"]; previous != nil {
		if _, ok := seen[asString(previous)]; !ok {
			*errors = append(*errors, fmt.Sprintf("pull %s previous is missing", digest))
		}
	}
	for _, key := range []string{"source_tip", "target_tip"} {
		if _, ok := seen[asString(document[key])]; !ok {
			*errors = append(*errors, fmt.Sprintf("pull %s %s is missing", digest, key))
		}
	}
	if asString(document["state"]) == "merged" {
		if _, ok := seen[asString(document["merge_commit"])]; !ok {
			*errors = append(*errors, fmt.Sprintf("pull %s merge commit is missing", digest))
		}
	}
}

func (r *MedRepo) requireRef(name string) (string, error) {
	target, err := r.store.ReadRef(name)
	if err != nil {
		return "", err
	}
	if target == "" {
		return "", mederr.Policyf("missing ref %s", name)
	}
	return target, nil
}

func (r *MedRepo) now() string {
	if r.Clock != nil {
		return r.Clock()
	}
	return UTCNow()
}

func requireHuman(actor Actor, action string) error {
	if actor.Role != "resident" && actor.Role != "attending" {
		return mederr.Policyf("only a clinician can %s", action)
	}
	return nil
}

func requireWriter(actor Actor) error {
	if actor.Role != "resident" && actor.Role != "attending" && actor.Role != "system" {
		return mederr.Policyf("role %s is read-only", actor.Role)
	}
	return nil
}

func hasIndependentApproval(pull map[string]any) bool {
	author := asString(asMap(pull["author"])["id"])
	for _, item := range asSlice(pull["reviews"]) {
		review := asMap(item)
		if asString(review["decision"]) != "approve" {
			continue
		}
		actor := asMap(review["actor"])
		if asString(actor["role"]) == "attending" && asString(actor["id"]) != author {
			return true
		}
	}
	return false
}

type observation struct {
	key    string
	value  string
	when   time.Time
	commit string
}

func observations(commits []map[string]any) ([]observation, error) {
	corrected := map[string]struct{}{}
	for _, commit := range commits {
		if asString(commit["type"]) == "correction" && asString(commit["corrects"]) != "" {
			corrected[asString(commit["corrects"])] = struct{}{}
		}
	}
	var found []observation
	for _, commit := range commits {
		hash := asString(commit["hash"])
		if _, ok := corrected[hash]; ok {
			continue
		}
		for _, item := range asSlice(commit["resources"]) {
			resource, ok := item.(map[string]any)
			if !ok || asString(resource["resourceType"]) != "Observation" {
				continue
			}
			code, _ := resource["code"].(map[string]any)
			coding := asSlice(code["coding"])
			var first map[string]any
			if len(coding) == 0 {
				first = map[string]any{}
			} else {
				first, _ = coding[0].(map[string]any)
				if first == nil {
					first = map[string]any{}
				}
			}
			quantityObj, _ := resource["valueQuantity"].(map[string]any)
			quantity, hasQuantity := quantityObj["value"]
			effective := asString(resource["effectiveDateTime"])
			if effective == "" || !hasQuantity || quantity == nil {
				continue
			}
			when, err := ParseTime(effective)
			if err != nil {
				return nil, err
			}
			found = append(found, observation{
				key:    pyNone(first["system"]) + "|" + pyNone(first["code"]),
				value:  fmt.Sprint(quantity),
				when:   when,
				commit: hash,
			})
		}
	}
	return found, nil
}

// ParseTime parses a record timestamp. A zone is required.
func ParseTime(value string) (time.Time, error) {
	if _, err := time.Parse("2006-01-02T15:04:05", value); err == nil {
		return time.Time{}, mederr.Integrityf("naive timestamp: %s", value)
	}
	text := value
	if strings.HasSuffix(text, "Z") {
		text = strings.TrimSuffix(text, "Z") + "+00:00"
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05.999999999Z07:00",
		time.RFC3339,
		time.RFC3339Nano,
	} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC(), nil
		}
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, mederr.Integrityf("bad timestamp: %s", value)
}

func newID(prefix string) string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(buf[:])
}

func withHash(digest string, document map[string]any) map[string]any {
	out := make(map[string]any, len(document)+1)
	for key, value := range document {
		out[key] = value
	}
	out["hash"] = digest
	return out
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

func stringsToAny(items []string) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}

func mapsToAny(items []map[string]any) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}

func pyNone(v any) string {
	if v == nil {
		return "None"
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func reverseMaps(items []map[string]any) {
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
}

func sortStrings(items []string) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j] < items[j-1]; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

func asStringKept(current any, fallback string) string {
	if fallback != "" {
		return fallback
	}
	return asString(current)
}
