// Package fhir builds the FHIR R4 subset MedGit stores and the export projection.
//
// Stored resources keep decimal quantities as strings so the object hash is
// stable. ProjectResource is the only place those strings become JSON numbers,
// and that projection is never hashed.
package fhir

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/aomarnevarez/medgit/internal/mederr"
)

const (
	// ActivitySystem is the code system for a commit type on Provenance.
	ActivitySystem = "https://medgit.dev/activity"
	// CommitSystem identifies a MedGit object hash.
	CommitSystem = "https://medgit.dev/commit"
	// RuleSystem identifies a published screening rule.
	RuleSystem = "https://medgit.dev/rules"
	// CorrectionExt links an exported resource to the commit that corrected it.
	CorrectionExt = "https://medgit.dev/fhir/StructureDefinition/corrected-by"
)

var decimal = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// PatientResource builds the synthetic Patient stored on patient-init.
func PatientResource(patientRef, display, gender, birthDate string) map[string]any {
	parts := strings.Fields(display)
	family := display
	given := []any{display}
	if len(parts) > 0 {
		family = parts[len(parts)-1]
		if len(parts) > 1 {
			given = make([]any, 0, len(parts)-1)
			for _, part := range parts[:len(parts)-1] {
				given = append(given, part)
			}
		}
	}
	return map[string]any{
		"resourceType": "Patient",
		"id":           patientRef,
		"identifier": []any{map[string]any{
			"system": "https://medgit.dev/synthetic",
			"value":  patientRef,
		}},
		"name": []any{map[string]any{
			"use":    "official",
			"family": family,
			"given":  given,
			"text":   display,
		}},
		"gender":    gender,
		"birthDate": birthDate,
		"meta": map[string]any{
			"security": []any{map[string]any{
				"system": "https://medgit.dev/synthetic",
				"code":   "synthetic",
			}},
		},
	}
}

// EncounterResource builds the Encounter stored on episode-open.
func EncounterResource(episodeID, patientRef, reason, encounterClass, start string) map[string]any {
	return map[string]any{
		"resourceType": "Encounter",
		"id":           episodeID,
		"status":       "in-progress",
		"class": map[string]any{
			"system": "http://terminology.hl7.org/CodeSystem/v3-ActCode",
			"code":   encounterClass,
		},
		"subject":    map[string]any{"reference": "Patient/" + patientRef},
		"reasonCode": []any{map[string]any{"text": reason}},
		"period":     map[string]any{"start": start},
	}
}

// QuantityObservation builds an Observation whose value is still a decimal string.
func QuantityObservation(resourceID, patientRef, episodeID, loinc, display, value, unit, ucum, effective string) (map[string]any, error) {
	if !decimal.MatchString(value) {
		return nil, mederr.Integrityf("observation value must be a decimal string, got '%s'", value)
	}
	return map[string]any{
		"resourceType": "Observation",
		"id":           resourceID,
		"status":       "final",
		"code": map[string]any{
			"coding": []any{map[string]any{
				"system":  "http://loinc.org",
				"code":    loinc,
				"display": display,
			}},
		},
		"subject":           map[string]any{"reference": "Patient/" + patientRef},
		"encounter":         map[string]any{"reference": "Encounter/" + episodeID},
		"effectiveDateTime": effective,
		"valueQuantity": map[string]any{
			"value":  value,
			"unit":   unit,
			"system": "http://unitsofmeasure.org",
			"code":   ucum,
		},
	}, nil
}

// DocumentReference builds a progress note.
func DocumentReference(resourceID, patientRef, episodeID, text, author, when string) map[string]any {
	return map[string]any{
		"resourceType": "DocumentReference",
		"id":           resourceID,
		"status":       "current",
		"type":         map[string]any{"text": "Progress note"},
		"subject":      map[string]any{"reference": "Patient/" + patientRef},
		"context": map[string]any{
			"encounter": []any{map[string]any{"reference": "Encounter/" + episodeID}},
		},
		"date":        when,
		"description": text,
		"author":      []any{map[string]any{"display": author}},
	}
}

// RiskAssessment builds the qSOFA screening result. There is no probabilityDecimal.
func RiskAssessment(resourceID, patientRef, episodeID string, score int, band, narrative, when string) map[string]any {
	return map[string]any{
		"resourceType":       "RiskAssessment",
		"id":                 resourceID,
		"status":             "final",
		"subject":            map[string]any{"reference": "Patient/" + patientRef},
		"encounter":          map[string]any{"reference": "Encounter/" + episodeID},
		"occurrenceDateTime": when,
		"method": map[string]any{
			"coding": []any{map[string]any{
				"system":  RuleSystem,
				"code":    "qsofa-ref-1",
				"display": "qSOFA reference rule (screening, not a diagnosis)",
			}},
		},
		"prediction": []any{map[string]any{
			"outcome": map[string]any{"text": fmt.Sprintf("qSOFA %d/3", score)},
			"qualitativeRisk": map[string]any{
				"coding": []any{map[string]any{
					"system": "http://terminology.hl7.org/CodeSystem/risk-probability",
					"code":   band,
				}},
			},
		}},
		"note": []any{map[string]any{"text": narrative}},
	}
}

// ProjectResource returns a standards-shaped copy. It is not an input to the object hash.
func ProjectResource(resource map[string]any, correctedBy string) map[string]any {
	projected := cloneMap(resource)
	restoreQuantities(projected)
	if correctedBy != "" {
		if _, ok := projected["status"]; ok {
			projected["status"] = "entered-in-error"
		}
		extensions := append([]any{}, asSlice(projected["extension"])...)
		extensions = append(extensions, map[string]any{
			"url":         CorrectionExt,
			"valueString": correctedBy,
		})
		projected["extension"] = extensions
	}
	return projected
}

// Provenance builds one Provenance resource for a commit.
func Provenance(commitHash string, commit map[string]any, targetRefs []string) map[string]any {
	author, _ := commit["author"].(map[string]any)
	var target []any
	if len(targetRefs) == 0 {
		target = []any{map[string]any{
			"identifier": map[string]any{
				"system": CommitSystem,
				"value":  commitHash,
			},
		}}
	} else {
		target = make([]any, len(targetRefs))
		for i, ref := range targetRefs {
			target[i] = map[string]any{"reference": ref}
		}
	}
	prefix := commitHash
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	return map[string]any{
		"resourceType":     "Provenance",
		"id":               "prov-" + prefix,
		"target":           target,
		"recorded":         commit["recorded_at"],
		"occurredDateTime": commit["authored_at"],
		"activity": map[string]any{
			"coding": []any{map[string]any{
				"system": ActivitySystem,
				"code":   commit["type"],
			}},
		},
		"agent": []any{map[string]any{
			"type": map[string]any{"text": author["role"]},
			"who": map[string]any{
				"display": author["display"],
				"identifier": map[string]any{
					"system": "https://medgit.dev/actor",
					"value":  author["id"],
				},
			},
		}},
		"entity": []any{map[string]any{
			"role": "source",
			"what": map[string]any{
				"identifier": map[string]any{
					"system": CommitSystem,
					"value":  commitHash,
				},
			},
		}},
	}
}

func restoreQuantities(node any) {
	switch t := node.(type) {
	case map[string]any:
		if raw, ok := t["value"].(string); ok {
			if _, hasUnit := t["unit"]; hasUnit && decimal.MatchString(raw) {
				t["value"] = asNumber(raw)
			}
		}
		for _, child := range t {
			restoreQuantities(child)
		}
	case []any:
		for _, child := range t {
			restoreQuantities(child)
		}
	}
}

func asNumber(value string) any {
	if strings.Contains(value, ".") {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return value
		}
		return parsed
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return value
	}
	return parsed
}

func cloneMap(m map[string]any) map[string]any {
	cloned, _ := clone(m).(map[string]any)
	return cloned
}

func clone(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for key, child := range t {
			out[key] = clone(child)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = clone(child)
		}
		return out
	default:
		return v
	}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
