// Package rules holds reference screeners with a published definition.
//
// These are not trained models and not diagnoses. A rule with a version string
// can be recomputed from the committed inputs.
package rules

import (
	"fmt"
	"strings"

	"github.com/aomarnevarez/medgit/internal/mederr"
)

// RuleID is the version string stored on every qSOFA commit.
const RuleID = "qsofa-ref-1"

// QSOFA is one evaluation of the reference screening rule.
type QSOFA struct {
	RespiratoryRate  int
	SystolicBP       int
	AlteredMentation bool
	Score            int
	Band             string
	Points           []string
	Rule             string
}

// Narrative is the text stored on the RiskAssessment. It names the band and
// states that the rule is not a diagnosis.
func (q QSOFA) Narrative() string {
	detail := "no qSOFA points"
	if len(q.Points) > 0 {
		detail = strings.Join(q.Points, "; ")
	}
	return fmt.Sprintf(
		"qSOFA %d/3 (%s). %s. Screening rule only — not a diagnosis of sepsis.",
		q.Score, q.Band, detail,
	)
}

// Score applies qSOFA. Respiratory rate and systolic pressure must sit inside
// a plausible range so a bad transmission cannot mint a screening result.
func Score(respiratoryRate, systolicBP int, alteredMentation bool) (QSOFA, error) {
	if respiratoryRate < 0 || respiratoryRate > 80 || systolicBP < 20 || systolicBP > 300 {
		return QSOFA{}, mederr.Policyf("vital sign is outside the plausible range for this rule")
	}
	var points []string
	score := 0
	if respiratoryRate >= 22 {
		score++
		points = append(points, fmt.Sprintf("respiratory rate %d >= 22", respiratoryRate))
	}
	if systolicBP <= 100 {
		score++
		points = append(points, fmt.Sprintf("systolic BP %d <= 100", systolicBP))
	}
	if alteredMentation {
		score++
		points = append(points, "altered mentation")
	}
	band := "high"
	switch {
	case score <= 1:
		band = "low"
	case score == 2:
		band = "moderate"
	}
	if points == nil {
		points = []string{}
	}
	return QSOFA{
		RespiratoryRate:  respiratoryRate,
		SystolicBP:       systolicBP,
		AlteredMentation: alteredMentation,
		Score:            score,
		Band:             band,
		Points:           points,
		Rule:             RuleID,
	}, nil
}
