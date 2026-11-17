package workflows

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestPRAgentCallerContract(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/pr-agent.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On map[string]struct {
			Types []string `yaml:"types"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Uses    string            `yaml:"uses"`
			With    map[string]string `yaml:"with"`
			Secrets map[string]string `yaml:"secrets"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	if got := workflow.On["pull_request"].Types; !reflect.DeepEqual(got, []string{"opened", "reopened", "ready_for_review", "synchronize"}) {
		t.Fatalf("unexpected lifecycle events: %v", got)
	}
	if len(workflow.On) != 2 || !reflect.DeepEqual(workflow.On["issue_comment"].Types, []string{"created"}) {
		t.Fatalf("unexpected command triggers: %v", workflow.On)
	}
	wantPermissions := map[string]string{"actions": "write", "contents": "read", "pull-requests": "write"}
	if !reflect.DeepEqual(workflow.Permissions, wantPermissions) {
		t.Fatalf("caller permission ceiling changed: %v", workflow.Permissions)
	}
	job := workflow.Jobs["review"]
	if len(workflow.Jobs) != 1 || !regexp.MustCompile(`^ranokay/agent-workflows/\.github/workflows/pr-agent\.yml@[0-9a-f]{40}$`).MatchString(job.Uses) {
		t.Fatalf("review must use an immutable shared workflow: %q", job.Uses)
	}
	var context []string
	if err := json.Unmarshal([]byte(job.With["context-files"]), &context); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(context, []string{"docs/agents/pr-review.md", "docs/current-state.md", "README.md"}) || len(job.With) != 1 {
		t.Fatalf("unexpected review context: %v", job.With)
	}
	if !reflect.DeepEqual(job.Secrets, map[string]string{"opencode-key": "${{ secrets.PR_AGENT_OPENCODE_KEY }}"}) {
		t.Fatalf("unexpected secret mapping: %v", job.Secrets)
	}
}

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
	run := func(name string, args ...string) ([]byte, error) {
		command := exec.Command(name, args...)
		command.Dir = directory
		command.Env = append(os.Environ(), "MISE_TRUSTED_CONFIG_PATHS="+directory)
		return command.CombinedOutput()
	}
	if output, err := run("git", "init", "--quiet"); err != nil {
		t.Fatalf("initialize fixture: %v\n%s", err, output)
	}
	valid := "name: Fixture\non: push\njobs:\n  check:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo checked\n"
	if err := os.WriteFile(filepath.Join(workflows, "existing.yml"), []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := run("mise", "run", "workflow-check"); err != nil {
		t.Fatalf("valid workflow failed: %v\n%s", err, output)
	}
	invalid := strings.Replace(valid, "echo checked", "${{ undefined.value }}", 1)
	if err := os.WriteFile(filepath.Join(workflows, "newly-added.yml"), []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := run("mise", "run", "workflow-check")
	if err == nil || !strings.Contains(string(output), "newly-added.yml") {
		t.Fatalf("new invalid workflow was not reported: error=%v\n%s", err, output)
	}
}
