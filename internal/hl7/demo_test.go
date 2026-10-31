package hl7_test

import (
	"strings"
	"testing"

	"github.com/aomarnevarez/medgit/internal/canonical"
	"github.com/aomarnevarez/medgit/internal/hl7"
)

func TestOfficialPatientCanonicalHash(t *testing.T) {
	patient, err := hl7.LoadOfficial("patient-example-f001-pieter.json")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := canonical.Hash(patient)
	if err != nil {
		t.Fatal(err)
	}
	const want = "0a7cedea5476da360e730d8e67ce3c06c24d9b64551471a06aa99b269a8887c7"
	if sum != want {
		t.Fatalf("patient hash = %s", sum)
	}
}

func TestOfficialResourcesRoundTrip(t *testing.T) {
	record, err := hl7.BuildF001(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := record.Verify()
	if err != nil || report["ok"] != true {
		t.Fatalf("verify = %#v %v", report, err)
	}
	config, err := record.Config()
	if err != nil {
		t.Fatal(err)
	}
	if config["patient_ref"] != "f001" || !strings.Contains(config["display"].(string), "Heuvel") {
		t.Fatalf("config = %#v", config)
	}
	bundle, err := record.ExportFHIR("")
	if err != nil {
		t.Fatal(err)
	}
	byType := map[string][]map[string]any{}
	for _, entry := range bundle["entry"].([]any) {
		resource := entry.(map[string]any)["resource"].(map[string]any)
		kind := resource["resourceType"].(string)
		byType[kind] = append(byType[kind], resource)
	}
	if byType["Patient"][0]["id"] != "f001" {
		t.Fatalf("patient = %#v", byType["Patient"][0]["id"])
	}
	var glucose map[string]any
	for _, item := range byType["Observation"] {
		coding := item["code"].(map[string]any)["coding"].([]any)
		if coding[0].(map[string]any)["code"] == "15074-8" {
			glucose = item
			break
		}
	}
	if glucose == nil {
		t.Fatal("missing glucose observation")
	}
	if glucose["valueQuantity"].(map[string]any)["value"] != 6.3 {
		t.Fatalf("glucose = %#v", glucose["valueQuantity"])
	}
	rows, err := record.Log("episodes/f001", false)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	for _, commit := range rows {
		for _, item := range commit["resources"].([]any) {
			resource := item.(map[string]any)
			if resource["resourceType"] == "Observation" {
				stored = commit
				break
			}
		}
		if stored != nil {
			break
		}
	}
	value := stored["resources"].([]any)[0].(map[string]any)["valueQuantity"].(map[string]any)["value"]
	if value != "6.3" {
		t.Fatalf("stored glucose = %#v", value)
	}
	messages, err := record.Log("main", true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range messages {
		if strings.Contains(row["message"].(string), "Heart valve") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a heart valve commit message")
	}
	if byType["Encounter"][0]["status"] != "finished" {
		t.Fatalf("encounter = %#v", byType["Encounter"][0]["status"])
	}
}
