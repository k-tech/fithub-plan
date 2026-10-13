package workflow

import (
	"errors"
	"strings"
	"testing"
)

const validWorkflow = `version: 1
name: verify
on:
  push:
    branches: [main]
  pull_request: {}
  workflow_dispatch: {}
jobs:
  test:
    runs-on: linux-x64
    timeout-minutes: 15
    steps:
      - name: Checkout exact revision
        builtin: checkout
      - name: Unit tests
        run: go test ./...
        env:
          CGO_ENABLED: "0"
      - name: Save binary
        builtin: upload-artifact
        with:
          name: server
          path: out/server
  lint:
    runs-on: linux-x64
    needs: [test]
    steps:
      - builtin: checkout
      - run: go vet ./...
`

func mustParse(t *testing.T, source string) Document {
	t.Helper()
	document, issues, err := Parse(".fithub/workflows/verify.yml", []byte(source))
	if err != nil {
		t.Fatalf("unexpected error: %v (issues: %+v)", err, issues)
	}
	return document
}

func mustReject(t *testing.T, source, wantPath string) []Issue {
	t.Helper()
	_, issues, err := Parse(".fithub/workflows/verify.yml", []byte(source))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
	if len(issues) == 0 {
		t.Fatal("a rejection must carry at least one issue")
	}
	if wantPath != "" {
		found := false
		for _, issue := range issues {
			if issue.Path == wantPath {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected an issue at %q, got %+v", wantPath, issues)
		}
	}
	for _, issue := range issues {
		if issue.Message == "" {
			t.Fatalf("issue without a message: %+v", issue)
		}
	}
	return issues
}

func TestParseAcceptsDeploymentSecretRef(t *testing.T) {
	document := mustParse(t, `version: 1
on: {push: {}}
jobs:
  deploy:
    runs-on: linux-x64
    secret-refs: [DEPLOY_SSH_KEY]
    steps:
      - run: ssh user@host ./deploy.sh
`)
	if len(document.Jobs) != 1 || len(document.Jobs[0].SecretRefs) != 1 || document.Jobs[0].SecretRefs[0] != "DEPLOY_SSH_KEY" {
		t.Fatalf("deployment secret ref was not parsed: %+v", document.Jobs)
	}
}

func TestParseAcceptsDocumentedExample(t *testing.T) {
	document := mustParse(t, validWorkflow)
	if document.Name != "verify" || document.Path != ".fithub/workflows/verify.yml" {
		t.Fatalf("unexpected document: %+v", document)
	}
	if len(document.Triggers) != 3 {
		t.Fatalf("triggers: %v", document.Triggers)
	}
	if len(document.Jobs) != 2 {
		t.Fatalf("jobs: %+v", document.Jobs)
	}
	var test *Job
	for i := range document.Jobs {
		if document.Jobs[i].ID == "test" {
			test = &document.Jobs[i]
		}
	}
	if test == nil || test.TimeoutMinutes != 15 || len(test.Steps) != 3 {
		t.Fatalf("test job: %+v", test)
	}
	if test.Steps[0].Builtin != BuiltinCheckout || test.Steps[1].Run != "go test ./..." || test.Steps[1].Env["CGO_ENABLED"] != "0" {
		t.Fatalf("steps: %+v", test.Steps)
	}
	if test.Steps[1].Condition != "success" {
		t.Fatalf("default condition: %q", test.Steps[1].Condition)
	}
	// The raw source is preserved so a run can record the exact definition.
	if document.Raw != validWorkflow {
		t.Fatal("raw source must be preserved for the run record")
	}
}

func TestParseDoesNotTreatOnAsBoolean(t *testing.T) {
	// YAML 1.2 keeps `on` a string. A YAML 1.1 parser would fold it to true and
	// silently drop the whole trigger section.
	document := mustParse(t, "version: 1\non:\n  push: {}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n")
	if len(document.Triggers) != 1 || document.Triggers[0] != "push" {
		t.Fatalf("triggers: %v", document.Triggers)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{"unknown top level", "version: 1\non: {push: {}}\nbogus: 1\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n"},
		{"uses", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - uses: actions/checkout@v4\n"},
		{"job level env", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    env:\n      FOO: bar\n    steps:\n      - run: echo hi\n"},
		{"strategy matrix", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    strategy:\n      matrix:\n        go: ['1.22']\n    steps:\n      - run: echo hi\n"},
		{"services", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    services:\n      db:\n        image: postgres\n    steps:\n      - run: echo hi\n"},
		{"defaults", "version: 1\non: {push: {}}\ndefaults:\n  run:\n    working-directory: sub\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustReject(t, tc.source, "")
		})
	}
}

func TestParseRejectsUnsupportedValues(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		wantPath string
	}{
		{"version 2", "version: 2\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n", "version"},
		{"missing version", "on: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n", "version"},
		{"no trigger", "version: 1\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n", "on"},
		{"unknown trigger", "version: 1\non:\n  schedule: {}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n", "on.schedule"},
		{"tag trigger", "version: 1\non:\n  push:\n    tags: [v1]\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n", "on.push.tags"},
		{"no jobs", "version: 1\non: {push: {}}\n", "jobs"},
		{"no steps", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps: []\n", "jobs.a.steps"},
		{"missing runs-on", "version: 1\non: {push: {}}\njobs:\n  a:\n    steps:\n      - run: echo hi\n", "jobs.a.runs-on"},
		{"unknown builtin", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - builtin: cache\n", "jobs.a.steps[0].builtin"},
		{"run and builtin", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n        builtin: checkout\n", "jobs.a.steps[0]"},
		{"neither run nor builtin", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - name: nothing\n", "jobs.a.steps[0]"},
		{"unknown needs", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    needs: [ghost]\n    steps:\n      - run: echo hi\n", "jobs.a.needs"},
		{"self need", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    needs: [a]\n    steps:\n      - run: echo hi\n", "jobs.a.needs"},
		{"cycle", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    needs: [b]\n    steps:\n      - run: echo hi\n  b:\n    runs-on: linux-x64\n    needs: [a]\n    steps:\n      - run: echo hi\n", "jobs"},
		{"expression in env", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n        env:\n          SHA: ${{ github.sha }}\n", "jobs.a.steps[0].env"},
		{"condition expression", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n        condition: ${{ success() }}\n", "jobs.a.steps[0].condition"},
		{"artifact without inputs", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - builtin: upload-artifact\n", "jobs.a.steps[0].with"},
		{"artifact traversal path", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - builtin: upload-artifact\n        with:\n          name: out\n          path: ../../etc\n", "jobs.a.steps[0].with.path"},
		{"artifact name with slash", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - builtin: upload-artifact\n        with:\n          name: a/b\n          path: out\n", "jobs.a.steps[0].with.name"},
		{"absolute working-directory", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n        working-directory: /etc\n", "jobs.a.steps[0].working-directory"},
		{"traversal working-directory", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n        working-directory: ../outside\n", "jobs.a.steps[0].working-directory"},
		{"timeout too large", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    timeout-minutes: 5000\n    steps:\n      - run: echo hi\n", "jobs.a.timeout-minutes"},
		{"bad job id", "version: 1\non: {push: {}}\njobs:\n  1bad:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n", "jobs.1bad"},
		{"bad secret name", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    secret-refs: [\"bad name\"]\n    steps:\n      - run: echo hi\n", "jobs.a.secret-refs"},
		{"unknown shell", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n        shell: zsh\n", "jobs.a.steps[0].shell"},
		{"shell on builtin", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - builtin: checkout\n        shell: sh\n", "jobs.a.steps[0].shell"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustReject(t, tc.source, tc.wantPath)
		})
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{"duplicate top level key", "version: 1\non: {push: {}}\non: {pull_request: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n"},
		{"duplicate nested key", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo one\n        run: echo two\n"},
		{"tab indentation", "version: 1\non: {push: {}}\njobs:\n\ta:\n\t\truns-on: linux-x64\n"},
		{"second document", "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n---\nversion: 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustReject(t, tc.source, "")
		})
	}
}

func TestParseRejectsOversizedInput(t *testing.T) {
	big := validWorkflow + strings.Repeat("# padding\n", MaxBytes/10)
	mustReject(t, big, "file")
}

func TestParseRejectsAliasExpansion(t *testing.T) {
	source := "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n"
	source += "      - &base\n        run: echo hi\n"
	for i := 0; i < MaxAliases+5; i++ {
		source += "      - *base\n"
	}
	mustReject(t, source, "file")
}

func TestParseRejectsTooManyJobs(t *testing.T) {
	source := "version: 1\non: {push: {}}\njobs:\n"
	for i := 0; i < MaxJobs+1; i++ {
		source += "  job" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ":\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n"
	}
	mustReject(t, source, "jobs")
}

func TestIsWorkflowPath(t *testing.T) {
	allowed := []string{
		".fithub/workflows/ci.yml",
		".fithub/workflows/ci.yaml",
		".fithub/workflows/nested-name.yml",
	}
	for _, candidate := range allowed {
		if !IsWorkflowPath(candidate) {
			t.Errorf("%q must be a workflow path", candidate)
		}
	}
	rejected := []string{
		"",
		".fithub/workflows/",
		".fithub/workflows/nested/ci.yml",
		".fithub/workflows/../ci.yml",
		".fithub/workflows/.hidden.yml",
		".fithub/workflows/ci.txt",
		".fithub/other/ci.yml",
		".fithub/workflowsci.yml",
		"workflows/ci.yml",
		"/.fithub/workflows/ci.yml",
		".fithub/workflows\\ci.yml",
	}
	for _, candidate := range rejected {
		if IsWorkflowPath(candidate) {
			t.Errorf("%q must not be a workflow path", candidate)
		}
	}
}

func TestParseRejectsInvalidPath(t *testing.T) {
	if _, _, err := Parse("ci.yml", []byte(validWorkflow)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for a path outside the workflow directory, got %v", err)
	}
}

func TestBranchPatterns(t *testing.T) {
	accepted := []string{"main", "release/*", "feature/x"}
	for _, pattern := range accepted {
		source := "version: 1\non:\n  push:\n    branches: [" + pattern + "]\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n"
		mustParse(t, source)
	}
	for _, pattern := range []string{"", "*", "ma*in", "re lease", "bad..branch", "/leading"} {
		source := "version: 1\non:\n  push:\n    branches: [\"" + pattern + "\"]\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n"
		mustReject(t, source, "on.push.branches")
	}
}

func TestConditions(t *testing.T) {
	for _, condition := range []string{"success", "failure", "always"} {
		source := "version: 1\non: {push: {}}\njobs:\n  a:\n    runs-on: linux-x64\n    steps:\n      - run: echo hi\n        condition: " + condition + "\n"
		document := mustParse(t, source)
		if document.Jobs[0].Steps[0].Condition != condition {
			t.Fatalf("condition %q not preserved", condition)
		}
	}
}
