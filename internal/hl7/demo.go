// Package hl7 loads the official FHIR R4 f001 examples into a MedGit record.
//
// The resources stay official. MedGit only decides who filed them and in what
// order. Decimal numbers are kept as strings so the object hash does not
// depend on floating point.
package hl7

import (
	"embed"
	"fmt"
	"strings"

	"github.com/aomarnevarez/medgit/internal/canonical"
	"github.com/aomarnevarez/medgit/internal/repo"
)

//go:embed data/f001/*.json
var files embed.FS

// Attending and Resident are the demo operators. They are not the clinical
// authors of the HL7 example.
var (
	Attending = mustActor("att-demo", "Attending (demo operator)", "attending")
	Resident  = mustActor("res-demo", "Resident (demo operator)", "resident")
)

var resourceTypes = map[string]string{
	"Observation":       "observation",
	"Condition":         "note",
	"Procedure":         "procedure",
	"ServiceRequest":    "order",
	"MedicationRequest": "medication",
	"DiagnosticReport":  "result",
	"DocumentReference": "note",
}

// LoadOfficial reads one vendored HL7 example. Decimal JSON numbers become strings.
func LoadOfficial(name string) (any, error) {
	raw, err := files.ReadFile("data/f001/" + name)
	if err != nil {
		return nil, err
	}
	return canonical.Decode(raw)
}

// PatientDisplay chooses the official name, then the first name.
func PatientDisplay(patient map[string]any) string {
	names := asSlice(patient["name"])
	if len(names) == 0 {
		names = []any{map[string]any{}}
	}
	chosen := asMap(names[0])
	for _, item := range names {
		candidate := asMap(item)
		if asString(candidate["use"]) == "official" {
			chosen = candidate
			break
		}
	}
	var givenParts []string
	for _, part := range asSlice(chosen["given"]) {
		if text, ok := part.(string); ok {
			givenParts = append(givenParts, text)
		}
	}
	given := strings.Join(givenParts, " ")
	family := asString(chosen["family"])
	text := asString(chosen["text"])
	if text == "" {
		text = strings.TrimSpace(given + " " + family)
	}
	if text == "" {
		text = asString(patient["id"])
	}
	if text == "" {
		text = "Patient"
	}
	return text
}

// ResourceLabel is a short title for a commit message.
func ResourceLabel(resource map[string]any) string {
	for _, key := range []string{"code", "medicationCodeableConcept", "type"} {
		if label := codedText(resource[key]); label != "" {
			return label
		}
	}
	return fmt.Sprintf("%s/%s", asString(resource["resourceType"]), asString(resource["id"]))
}

// BuildF001 creates the Pieter van de Heuvel record from the vendored HL7 files.
func BuildF001(root string, clock func() string) (*repo.MedRepo, error) {
	patientValue, err := LoadOfficial("patient-example-f001-pieter.json")
	if err != nil {
		return nil, err
	}
	patient, _ := patientValue.(map[string]any)
	encounterValue, err := LoadOfficial("encounter-example-f001-heart.json")
	if err != nil {
		return nil, err
	}
	encounter, _ := encounterValue.(map[string]any)
	gender := asString(patient["gender"])
	if gender == "" {
		gender = "unknown"
	}
	birthDate := asString(patient["birthDate"])
	if birthDate == "" {
		birthDate = "1970-01-01"
	}
	record, err := repo.Init(root, Attending, asString(patient["id"]), PatientDisplay(patient), gender, birthDate, clock, []any{patient})
	if err != nil {
		return nil, err
	}
	classCode := asString(asMap(encounter["class"])["code"])
	if classCode == "" {
		classCode = "AMB"
	}
	opened, err := record.OpenEpisode(Resident, ResourceLabel(encounter), classCode, asString(encounter["id"]), []any{encounter})
	if err != nil {
		return nil, err
	}
	episodeID := asString(opened["episode_id"])
	for _, name := range []string{
		"condition-example-f001-heart.json",
		"procedure-example-f001-heart.json",
		"observation-example-f001-glucose.json",
	} {
		resource, err := loadMap(name)
		if err != nil {
			return nil, err
		}
		if _, err := commitOne(record, episodeID, resource, Resident); err != nil {
			return nil, err
		}
	}
	bundle, err := loadMap("diagnosticreport-example-f001-bloodexam.json")
	if err != nil {
		return nil, err
	}
	for _, entry := range asSlice(bundle["entry"]) {
		resource, ok := asMap(entry)["resource"].(map[string]any)
		if !ok || asString(resource["resourceType"]) == "" {
			continue
		}
		if _, err := commitOne(record, episodeID, resource, Resident); err != nil {
			return nil, err
		}
	}
	consult, err := record.OpenPull(
		Resident,
		episodeID,
		"HL7 f001 — integrate the sample cardiology episode",
		"Official FHIR R4 resources. The demo operator is not the clinical author of the example.",
	)
	if err != nil {
		return nil, err
	}
	if _, err := record.Review(Attending, asString(consult["id"]), "approve", "Integrate the tip. The HL7 resources are not rewritten."); err != nil {
		return nil, err
	}
	if _, err := record.Merge(Attending, asString(consult["id"]), false); err != nil {
		return nil, err
	}
	return record, nil
}

// CommitResource appends one FHIR resource to an open episode. Decimal numbers
// in the resource are stored as strings. This does not merge.
func CommitResource(record *repo.MedRepo, actor repo.Actor, episodeID string, resource map[string]any) (map[string]any, error) {
	normalized, err := canonical.CoerceValue(resource)
	if err != nil {
		return nil, err
	}
	coerced, ok := normalized.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("resource must be a FHIR object")
	}
	return commitOne(record, episodeID, coerced, actor)
}

func loadMap(name string) (map[string]any, error) {
	value, err := LoadOfficial(name)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s is not a JSON object", name)
	}
	return object, nil
}

func commitOne(record *repo.MedRepo, episodeID string, resource map[string]any, actor repo.Actor) (map[string]any, error) {
	commitType, ok := resourceTypes[asString(resource["resourceType"])]
	if !ok {
		commitType = "note"
	}
	return record.Commit(actor, episodeID, commitType, ResourceLabel(resource), []any{resource}, "normal", "", "")
}

func codedText(coded any) string {
	if list, ok := coded.([]any); ok {
		if len(list) == 0 {
			return ""
		}
		coded = list[0]
	}
	object, ok := coded.(map[string]any)
	if !ok {
		return ""
	}
	if text, ok := object["text"].(string); ok && text != "" {
		return text
	}
	coding := asSlice(object["coding"])
	if len(coding) == 0 {
		return ""
	}
	first := asMap(coding[0])
	if display := asString(first["display"]); display != "" {
		return display
	}
	return asString(first["code"])
}

func mustActor(id, display, role string) repo.Actor {
	actor, err := repo.NewActor(id, display, role)
	if err != nil {
		panic(err)
	}
	return actor
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
