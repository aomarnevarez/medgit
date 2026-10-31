// Command sepsis walks a synthetic emergency-department chart.
//
// No real patient data. Run from the repository root:
//
//	go run ./examples/sepsis
package main

import (
	"fmt"
	"os"

	"github.com/aomarnevarez/medgit/internal/repo"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "sepsis: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	attending, err := repo.NewActor("att-ramirez", "Elena Ramirez, MD", "attending")
	if err != nil {
		return err
	}
	resident, err := repo.NewActor("res-ortega", "Luis Ortega, MD", "resident")
	if err != nil {
		return err
	}
	lab, err := repo.NewActor("bot-lis", "LIS", "system")
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "medgit-sepsis-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	record, err := repo.Init(directory, attending, "syn-pat-001", "Synthetic Patient One", "female", "1978-04-12", nil, nil)
	if err != nil {
		return err
	}
	status, err := record.Status()
	if err != nil {
		return err
	}
	fmt.Printf("record opened at %s  main=%s\n", repo.UTCNow(), short(asString(status["main"])))
	episode, err := record.OpenEpisode(resident, "Emergency department admission. Dyspnea and suspected infection.", "EMER", "", nil)
	if err != nil {
		return err
	}
	episodeID := asString(episode["episode_id"])
	fmt.Printf("episode %s opened by resident\n", episodeID)
	if _, err := record.CommitObservation(resident, episodeID, "8480-6", "Systolic blood pressure", "88", "mmHg", "mm[Hg]", "", "", ""); err != nil {
		return err
	}
	if _, err := record.CommitObservation(resident, episodeID, "9279-1", "Respiratory rate", "24", "/min", "/min", "", "", ""); err != nil {
		return err
	}
	if _, err := record.CommitNote(resident, episodeID, "Patient is drowsy. Lactate and attending review requested.", ""); err != nil {
		return err
	}
	wrong, err := record.CommitObservation(lab, episodeID, "2524-5", "Lactate", "2.1", "mmol/L", "mmol/L", "Lactate 2.1 mmol/L", "", "result")
	if err != nil {
		return err
	}
	if _, err := record.Commit(lab, episodeID, "correction", "Transmission correction: lactate 4.2 mmol/L", []any{
		map[string]any{
			"resourceType": "Observation",
			"id":           "lac-corrected",
			"status":       "final",
			"code": map[string]any{
				"coding": []any{map[string]any{
					"system":  "http://loinc.org",
					"code":    "2524-5",
					"display": "Lactate",
				}},
			},
			"subject":           map[string]any{"reference": "Patient/syn-pat-001"},
			"effectiveDateTime": wrong["authored_at"],
			"valueQuantity": map[string]any{
				"value":  "4.2",
				"unit":   "mmol/L",
				"system": "http://unitsofmeasure.org",
				"code":   "mmol/L",
			},
		},
	}, "normal", asString(wrong["hash"]), ""); err != nil {
		return err
	}
	fmt.Println("lab corrected its own result; the original object is still addressable")
	inference, err := record.CommitQSOFA(lab, episodeID, 24, 88, true, "", "")
	if err != nil {
		return err
	}
	fmt.Println(asString(inference["message"]))
	if err := record.Rebase(); err != nil {
		fmt.Printf("rebase refused: %s\n", err)
	}
	consult, err := record.OpenPull(
		resident,
		episodeID,
		"qSOFA 3/3. Request integration into the longitudinal record.",
		"Screening rule, not a diagnosis. Lactate corrected to 4.2.",
	)
	if err != nil {
		return err
	}
	if _, err := record.Review(attending, asString(consult["id"]), "approve", "Integrate. Start the sepsis bundle on this episode of care."); err != nil {
		return err
	}
	merged, err := record.Merge(attending, asString(consult["id"]), false)
	if err != nil {
		return err
	}
	report, err := record.Verify()
	if err != nil {
		return err
	}
	bundle, err := record.ExportFHIR("")
	if err != nil {
		return err
	}
	entries, _ := bundle["entry"].([]any)
	fmt.Printf("merged %s  verify=%v  bundle_entries=%d\n", short(asString(merged["hash"])), report["ok"], len(entries))
	fmt.Println("--- first-parent log ---")
	rows, err := record.Log("main", false)
	if err != nil {
		return err
	}
	for _, row := range rows {
		fmt.Printf("%s  %-14s %s\n", short(asString(row["hash"])), asString(row["type"]), asString(row["message"]))
	}
	return nil
}

func short(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
