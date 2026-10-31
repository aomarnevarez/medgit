package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aomarnevarez/medgit/internal/cli"
)

func TestCLIInitVerifyAndRefusedRebase(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{
		"-C", dir,
		"init",
		"--patient-ref", "syn-pat-001",
		"--display", "Synthetic Patient One",
		"--actor-id", "att-ramirez",
		"--actor-display", "Elena Ramirez, MD",
		"--actor-role", "attending",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init code %d stderr %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{"-C", dir, "verify"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("verify code %d stderr %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ok") {
		t.Fatalf("stdout = %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{"-C", dir, "rebase"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "rebase") {
		t.Fatalf("rebase code %d stderr %s", code, stderr.String())
	}
}
