package rules

import "testing"

func TestQSOFAHighBandIsNotAHiddenProbability(t *testing.T) {
	scored, err := Score(24, 88, true)
	if err != nil {
		t.Fatal(err)
	}
	if scored.Score != 3 || scored.Band != "high" {
		t.Fatalf("score = %+v", scored)
	}
	if !contains(scored.Narrative(), "not a diagnosis") {
		t.Fatalf("narrative = %s", scored.Narrative())
	}
	low, err := Score(18, 120, false)
	if err != nil {
		t.Fatal(err)
	}
	if low.Score != 0 || low.Band != "low" {
		t.Fatalf("low = %+v", low)
	}
}

func TestQSOFARejectsImplausibleVitals(t *testing.T) {
	if _, err := Score(90, 120, false); err == nil {
		t.Fatal("expected a range error")
	}
}

func contains(text, part string) bool {
	return len(text) >= len(part) && (text == part || len(part) == 0 ||
		func() bool {
			for i := 0; i+len(part) <= len(text); i++ {
				if text[i:i+len(part)] == part {
					return true
				}
			}
			return false
		}())
}
