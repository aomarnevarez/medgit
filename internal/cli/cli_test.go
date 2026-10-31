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

func TestReadmeCommands(t *testing.T) {
	root := t.TempDir()
	dir := root + "/syn-pat-001"
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{
		"init", dir,
		"--patient-ref", "syn-pat-001",
		"--display", "Synthetic Patient One",
		"--actor-id", "att-ramirez",
		"--actor-display", "Elena Ramirez, MD",
		"--actor-role", "attending",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatal(stderr.String())
	}
	if !strings.Contains(stdout.String(), "created "+dir) || !strings.Contains(stdout.String(), "main ") {
		t.Fatalf("init stdout = %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{
		"-C", dir, "episode", "open",
		"--reason", "Emergency department admission",
		"--actor-id", "res-ortega",
		"--actor-display", "Luis Ortega, MD",
		"--actor-role", "resident",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatal(stderr.String())
	}
	episodeID := idAfter(t, stdout.String(), "episode ")
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{
		"-C", dir, "note",
		"--episode", episodeID,
		"--text", "Abdominal pain and dyspnea.",
		"--actor-id", "res-ortega",
		"--actor-display", "Luis Ortega, MD",
		"--actor-role", "resident",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatal(stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{
		"-C", dir, "consult", "open",
		"--episode", episodeID,
		"--title", "Admit and integrate",
		"--actor-id", "res-ortega",
		"--actor-display", "Luis Ortega, MD",
		"--actor-role", "resident",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatal(stderr.String())
	}
	consultID := idAfter(t, stdout.String(), "consult ")
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{
		"-C", dir, "consult", "review", consultID,
		"--decision", "approve",
		"--comment", "Agreed, integrate.",
		"--actor-id", "att-ramirez",
		"--actor-display", "Elena Ramirez, MD",
		"--actor-role", "attending",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("review code %d stderr %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{
		"-C", dir, "consult", "merge", consultID,
		"--actor-id", "att-ramirez",
		"--actor-display", "Elena Ramirez, MD",
		"--actor-role", "attending",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("merge code %d stderr %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{"-C", dir, "status"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "merged") {
		t.Fatalf("status code %d\n%s\n%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{"-C", dir, "log"}, &stdout, &stderr)
	if code != 0 || strings.Contains(stdout.String(), "Abdominal pain") {
		t.Fatalf("first-parent log should omit the visit note:\n%s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{"-C", dir, "log", "--all"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "Abdominal pain and dyspnea.") {
		t.Fatalf("log --all code %d\n%s\n%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{"-C", dir, "verify"}, &stdout, &stderr)
	if code != 0 || !strings.HasPrefix(stdout.String(), "ok  objects=") {
		t.Fatalf("verify code %d stdout %s stderr %s", code, stdout.String(), stderr.String())
	}
}

func idAfter(t *testing.T, stdout, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("no %q line in %s", prefix, stdout)
	return ""
}

func TestEpisodeOpenPrintsTheIdToCopy(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{
		"-C", dir, "init",
		"--patient-ref", "syn-pat-001",
		"--display", "Synthetic Patient One",
		"--actor-id", "att-ramirez",
		"--actor-display", "Elena Ramirez, MD",
		"--actor-role", "attending",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatal(stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{
		"-C", dir, "episode", "open",
		"--reason", "Emergency department admission",
		"--actor-id", "res-ortega",
		"--actor-display", "Luis Ortega, MD",
		"--actor-role", "resident",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatal(stderr.String())
	}
	if !strings.Contains(stdout.String(), "episode ep_") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}
