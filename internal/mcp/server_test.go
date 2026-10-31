package mcp_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aomarnevarez/medgit/internal/mcp"
	"github.com/aomarnevarez/medgit/internal/repo"
)

func TestVerifyToolOverStdio(t *testing.T) {
	dir := t.TempDir()
	attending, err := repo.NewActor("att-ramirez", "Elena Ramirez, MD", "attending")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Init(dir, attending, "syn-pat-001", "Synthetic Patient One", "female", "1978-04-12", nil, nil); err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"medgit_verify","arguments":{}}}`)
	framed := "Content-Length: " + itoa(len(request)) + "\r\n\r\n" + string(request)
	var stdout bytes.Buffer
	if err := mcp.Serve(strings.NewReader(framed), &stdout, dir); err != nil {
		t.Fatal(err)
	}
	text := stdout.String()
	if !strings.Contains(text, "Content-Length:") {
		t.Fatalf("stdout = %s", text)
	}
	body := strings.SplitN(text, "\r\n\r\n", 2)[1]
	var payload struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Result.IsError || !strings.Contains(payload.Result.Content[0].Text, `"ok": true`) {
		t.Fatalf("payload = %#v", payload.Result)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
