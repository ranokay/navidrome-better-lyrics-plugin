//go:build workflowintegration

package workflows

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowCheckDiscoversNewWorkflows(t *testing.T) {
	// Exercise the actual task so an explicit filename list cannot silently omit future workflows.
	directory := t.TempDir()
	config, err := os.ReadFile("../../mise.toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "mise.toml"), config, 0600); err != nil {
		t.Fatal(err)
	}
	workflows := filepath.Join(directory, ".github", "workflows")
	if err := os.MkdirAll(workflows, 0700); err != nil {
		t.Fatal(err)
	}
	initialize := exec.Command("git", "init", "--quiet")
	initialize.Dir = directory
	if output, err := initialize.CombinedOutput(); err != nil {
		t.Fatalf("initialize fixture: %v\n%s", err, output)
	}
	checkWorkflows := func() ([]byte, error) {
		command := exec.Command("mise", "run", "workflow-check")
		command.Dir = directory
		command.Env = append(os.Environ(), "MISE_TRUSTED_CONFIG_PATHS="+directory, "MISE_AUTO_INSTALL=false")
		return command.CombinedOutput()
	}
	valid := "name: Fixture\non: push\njobs:\n  check:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo checked\n"
	if err := os.WriteFile(filepath.Join(workflows, "existing.yml"), []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := checkWorkflows(); err != nil {
		t.Fatalf("valid workflow failed: %v\n%s", err, output)
	}
	invalid := strings.Replace(valid, "echo checked", "${{ undefined.value }}", 1)
	if err := os.WriteFile(filepath.Join(workflows, "newly-added.yml"), []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := checkWorkflows()
	if err == nil || !strings.Contains(string(output), "newly-added.yml") {
		t.Fatalf("new invalid workflow was not reported: error=%v\n%s", err, output)
	}
}
