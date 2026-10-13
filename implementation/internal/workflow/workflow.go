// Package workflow parses and validates the FitHub Workflow v1 file format.
//
// It deliberately implements a small, explicitly versioned schema rather than a
// GitHub Actions compatibility layer: unknown fields, unknown step kinds, and
// unsupported constructs are rejected at enqueue time. A workflow file that
// cannot be understood must never be silently treated as "nothing to run",
// because that would show a green check for a definition nobody validated.
//
// The parser is used by the control plane only. It never executes anything and
// never resolves a path against the filesystem.
package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Limits keep a hostile or careless workflow from exhausting the control plane
// before any job runs.
const (
	// MaxBytes is the largest accepted workflow file.
	MaxBytes = 128 << 10
	// MaxJobs bounds the static job graph.
	MaxJobs = 32
	// MaxSteps bounds the steps of one job.
	MaxSteps = 64
	// MaxNeeds bounds the fan-in of one job.
	MaxNeeds = 8
	// MaxAliases bounds YAML alias expansion, which can otherwise blow up
	// memory before any validation runs.
	MaxAliases = 100
	// MaxTimeoutMinutes bounds a single job.
	MaxTimeoutMinutes = 120
)

// FilePrefix is the only directory that may define workflows. Workflow changes
// are restricted to repository administrators on every write path.
const FilePrefix = ".fithub/workflows/"

// ErrInvalid marks a workflow that must be rejected with its reason.
var ErrInvalid = errors.New("workflow: invalid definition")

// Severity separates a hard rejection from a warning an operator should still
// see. Nothing with an error may be enqueued.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Issue is one validation problem with a stable path for the UI.
type Issue struct {
	Severity Severity `json:"severity"`
	Path     string   `json:"path"`
	Message  string   `json:"message"`
}

// Document is a validated workflow definition.
type Document struct {
	// Name is the workflow's display name; defaults to the file name.
	Name string
	// Path is the repository-relative workflow file path.
	Path string
	// Raw is the exact source, kept so the run can record the definition it used.
	Raw string
	// Triggers lists the supported trigger names found in `on`.
	Triggers []string
	// Jobs is the validated job graph in declaration order.
	Jobs []Job
}

// Job is one validated job.
type Job struct {
	ID string
	// Name is the display name; the job ID is used when it is omitted.
	Name           string
	RunsOn         string
	TimeoutMinutes int
	Needs          []string
	SecretRefs     []string
	Steps          []Step
}

// Step is one validated step. Exactly one of Run or Builtin is set.
type Step struct {
	Name             string
	Run              string
	Builtin          string
	Shell            string
	With             map[string]string
	Env              map[string]string
	WorkingDirectory string
	TimeoutMinutes   int
	Condition        string
}

// BuiltinCheckout and BuiltinUploadArtifact are the only built-in steps in v1.
const (
	BuiltinCheckout       = "checkout"
	BuiltinUploadArtifact = "upload-artifact"
)

// ShellBash and ShellSh are the only interpreters a run step may select. Images
// that carry only a POSIX shell are common, so the shell belongs to the workflow
// rather than being a fixed choice made by the runner.
const (
	ShellBash = "bash"
	ShellSh   = "sh"
)

// supportedTriggers are the events v1 can trigger on.
var supportedTriggers = map[string]bool{
	"push":              true,
	"pull_request":      true,
	"workflow_dispatch": true,
}

// supportedConditions are the only step conditions in v1. Arbitrary expressions
// are not evaluated.
var supportedConditions = map[string]bool{
	"success": true,
	"failure": true,
	"always":  true,
}

var (
	// jobIDPattern follows the safe subset used for job identifiers. It must
	// stay compatible with needs references and with the FITHUB_JOB variable.
	jobIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	// secretRefPattern constrains secret names; the value never appears here.
	secretRefPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
	// runsOnPattern constrains runner labels to an administrator-published set.
	runsOnPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
)

// Parse validates a workflow document. path must be a repository-relative path
// inside FilePrefix. A returned Document always has at least one job and a
// non-empty trigger list; on error the returned issues explain every problem.
func Parse(path string, source []byte) (Document, []Issue, error) {
	if !IsWorkflowPath(path) {
		return Document{}, []Issue{{Severity: SeverityError, Path: "path", Message: "workflow files must live under " + FilePrefix}}, ErrInvalid
	}
	if len(source) == 0 {
		return Document{}, []Issue{{Severity: SeverityError, Path: "file", Message: "workflow file is empty"}}, ErrInvalid
	}
	if len(source) > MaxBytes {
		return Document{}, []Issue{{Severity: SeverityError, Path: "file", Message: fmt.Sprintf("workflow exceeds %d bytes", MaxBytes)}}, ErrInvalid
	}
	var issues []Issue
	if err := checkYAMLShape(source, &issues); err != nil {
		return Document{}, issues, err
	}
	var raw rawWorkflow
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		issues = append(issues, Issue{Severity: SeverityError, Path: "file", Message: describeYAMLError(err)})
		return Document{}, issues, ErrInvalid
	}
	// A workflow file must hold exactly one document; a second document would
	// otherwise be ignored while the first ran.
	var extra rawWorkflow
	if err := decoder.Decode(&extra); err != io.EOF {
		issues = append(issues, Issue{Severity: SeverityError, Path: "file", Message: "workflow must contain exactly one YAML document"})
		return Document{}, issues, ErrInvalid
	}
	document, issues := validate(path, string(source), raw)
	if len(issues) > 0 {
		return Document{}, issues, ErrInvalid
	}
	return document, nil, nil
}

// IsWorkflowPath reports whether a repository path may define a workflow. Only
// the direct children of FilePrefix are accepted, so a nested file cannot be
// mistaken for a definition and the admin-only rule covers everything the
// loader will read.
func IsWorkflowPath(candidate string) bool {
	if !strings.HasPrefix(candidate, FilePrefix) {
		return false
	}
	rest := strings.TrimPrefix(candidate, FilePrefix)
	if rest == "" || strings.Contains(rest, "/") || strings.Contains(rest, "\\") {
		return false
	}
	if rest == "." || rest == ".." || strings.HasPrefix(rest, ".") {
		return false
	}
	if !strings.HasSuffix(rest, ".yml") && !strings.HasSuffix(rest, ".yaml") {
		return false
	}
	// path.Clean must not change the value, which rejects any traversal or
	// redundant separator that survived the checks above.
	return path.Clean(candidate) == candidate && strings.HasPrefix(candidate, ".fithub/")
}

// checkYAMLShape rejects constructs that are legal YAML but unsafe or
// unsupported: excessive alias expansion, excessive nesting, and duplicate
// mapping keys. Duplicate keys are verified here because key conflicts inside
// nested mappings must never be resolved silently.
func checkYAMLShape(source []byte, issues *[]Issue) error {
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	aliases, depth := 0, 0
	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				return nil
			}
			*issues = append(*issues, Issue{Severity: SeverityError, Path: "file", Message: describeYAMLError(err)})
			return ErrInvalid
		}
		var walk func(*yaml.Node, int)
		walk = func(current *yaml.Node, level int) {
			if current == nil {
				return
			}
			if level > depth {
				depth = level
			}
			if current.Kind == yaml.AliasNode {
				aliases++
			}
			for _, child := range current.Content {
				walk(child, level+1)
			}
		}
		walk(&node, 1)
		if aliases > MaxAliases {
			*issues = append(*issues, Issue{Severity: SeverityError, Path: "file", Message: fmt.Sprintf("workflow uses more than %d YAML aliases", MaxAliases)})
			return ErrInvalid
		}
		if depth > 32 {
			*issues = append(*issues, Issue{Severity: SeverityError, Path: "file", Message: "workflow nesting is too deep"})
			return ErrInvalid
		}
	}
}

// describeYAMLError keeps the parser's message but drops the Go type names that
// a workflow author cannot act on.
func describeYAMLError(err error) string {
	message := err.Error()
	message = strings.ReplaceAll(message, "\n  ", "; ")
	return message
}

func validate(filePath, source string, raw rawWorkflow) (Document, []Issue) {
	var issues []Issue
	if raw.Version != 1 {
		issues = append(issues, Issue{Severity: SeverityError, Path: "version", Message: fmt.Sprintf("version must be 1, got %d", raw.Version)})
	}
	triggers := validateTriggers(raw.On, &issues)
	if len(raw.Jobs) == 0 {
		issues = append(issues, Issue{Severity: SeverityError, Path: "jobs", Message: "workflow must define at least one job"})
	}
	if len(raw.Jobs) > MaxJobs {
		issues = append(issues, Issue{Severity: SeverityError, Path: "jobs", Message: fmt.Sprintf("workflow defines more than %d jobs", MaxJobs)})
	}
	document := Document{Name: raw.Name, Path: filePath, Raw: source, Triggers: triggers}
	if document.Name == "" {
		document.Name = path.Base(filePath)
	}
	if len(document.Name) > 128 {
		issues = append(issues, Issue{Severity: SeverityError, Path: "name", Message: "name must be at most 128 characters"})
	}
	jobIndex := make(map[string]bool, len(raw.Jobs))
	for id := range raw.Jobs {
		if !jobIDPattern.MatchString(id) {
			issues = append(issues, Issue{Severity: SeverityError, Path: "jobs." + id, Message: "job id must start with a letter and use letters, digits, '-' or '_'"})
			continue
		}
		jobIndex[id] = true
	}
	for id, rawJob := range raw.Jobs {
		if !jobIDPattern.MatchString(id) {
			continue
		}
		job := validateJob(id, rawJob, jobIndex, &issues)
		document.Jobs = append(document.Jobs, job)
	}
	if hasCycle(document.Jobs) {
		issues = append(issues, Issue{Severity: SeverityError, Path: "jobs", Message: "job needs form a cycle"})
	}
	return document, issues
}

func validateTriggers(raw map[string]rawTrigger, issues *[]Issue) []string {
	if len(raw) == 0 {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: "on", Message: "workflow must declare at least one trigger"})
		return nil
	}
	names := make([]string, 0, len(raw))
	for name, trigger := range raw {
		if !supportedTriggers[name] {
			*issues = append(*issues, Issue{Severity: SeverityError, Path: "on." + name, Message: "unsupported trigger"})
			continue
		}
		names = append(names, name)
		if name != "push" {
			continue
		}
		for _, branch := range trigger.Branches {
			if !validBranchPattern(branch) {
				*issues = append(*issues, Issue{Severity: SeverityError, Path: "on.push.branches", Message: "invalid branch name: " + branch})
			}
		}
		if len(trigger.Tags) > 0 {
			*issues = append(*issues, Issue{Severity: SeverityError, Path: "on.push.tags", Message: "tag triggers are not supported in v1"})
		}
		if len(trigger.Paths) > 0 {
			*issues = append(*issues, Issue{Severity: SeverityError, Path: "on.push.paths", Message: "path filters are not supported in v1"})
		}
	}
	return names
}

func validateJob(id string, job rawJob, known map[string]bool, issues *[]Issue) Job {
	prefix := "jobs." + id + "."
	if job.Name != "" && len(job.Name) > 128 {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "name", Message: "job name must be at most 128 characters"})
	}
	if job.RunsOn == "" {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "runs-on", Message: "runs-on is required"})
	} else if !runsOnPattern.MatchString(job.RunsOn) {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "runs-on", Message: "runs-on label is not valid"})
	}
	result := Job{ID: id, Name: job.Name, RunsOn: job.RunsOn, TimeoutMinutes: job.TimeoutMinutes}
	if result.TimeoutMinutes < 0 || result.TimeoutMinutes > MaxTimeoutMinutes {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "timeout-minutes", Message: fmt.Sprintf("timeout-minutes must be between 1 and %d", MaxTimeoutMinutes)})
	}
	if len(job.Needs) > MaxNeeds {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "needs", Message: fmt.Sprintf("at most %d needs are allowed", MaxNeeds)})
	}
	seenNeeds := map[string]bool{}
	for _, need := range job.Needs {
		if need == id {
			*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "needs", Message: "a job cannot need itself"})
			continue
		}
		if !known[need] {
			*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "needs", Message: "needs references an unknown job: " + need})
			continue
		}
		if seenNeeds[need] {
			*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "needs", Message: "duplicate needs entry: " + need})
			continue
		}
		seenNeeds[need] = true
		result.Needs = append(result.Needs, need)
	}
	for _, secret := range job.SecretRefs {
		if !secretRefPattern.MatchString(secret) {
			*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "secret-refs", Message: "invalid secret name: " + secret})
			continue
		}
		result.SecretRefs = append(result.SecretRefs, secret)
	}
	if len(job.Steps) == 0 {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "steps", Message: "job must define at least one step"})
	}
	if len(job.Steps) > MaxSteps {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "steps", Message: fmt.Sprintf("at most %d steps are allowed", MaxSteps)})
	}
	for index, step := range job.Steps {
		result.Steps = append(result.Steps, validateStep(fmt.Sprintf("%ssteps[%d].", prefix, index), step, issues))
	}
	return result
}

func validateStep(prefix string, step rawStep, issues *[]Issue) Step {
	// Issues about the step as a whole point at the step, not at a child field,
	// so strip the trailing separator used to build child paths.
	stepPath := strings.TrimSuffix(prefix, ".")
	result := Step{
		Name:             step.Name,
		Run:              step.Run,
		Builtin:          step.Builtin,
		Shell:            step.Shell,
		Env:              step.Env,
		WorkingDirectory: step.WorkingDirectory,
		TimeoutMinutes:   step.TimeoutMinutes,
		Condition:        step.Condition,
	}
	if step.Name != "" && len(step.Name) > 128 {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "name", Message: "step name must be at most 128 characters"})
	}
	// The shell is an explicit allowlist rather than a free-form command: a step
	// only picks which interpreter runs the script, never what executable runs.
	if step.Shell != "" && step.Shell != ShellBash && step.Shell != ShellSh {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "shell", Message: "shell must be bash or sh"})
	}
	if step.Shell != "" && step.Builtin != "" {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "shell", Message: "shell is only valid on a run step"})
	}
	switch {
	case step.Run == "" && step.Builtin == "":
		*issues = append(*issues, Issue{Severity: SeverityError, Path: stepPath, Message: "step must set either run or builtin"})
	case step.Run != "" && step.Builtin != "":
		*issues = append(*issues, Issue{Severity: SeverityError, Path: stepPath, Message: "step cannot set both run and builtin"})
	case step.Builtin != "" && step.Builtin != BuiltinCheckout && step.Builtin != BuiltinUploadArtifact:
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "builtin", Message: "unsupported builtin: " + step.Builtin})
	}
	if len(step.Run) > 64<<10 {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "run", Message: "run script is too long"})
	}
	if step.Condition == "" {
		result.Condition = "success"
	} else if !supportedConditions[step.Condition] {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "condition", Message: "condition must be success, failure, or always"})
	}
	if step.TimeoutMinutes < 0 || step.TimeoutMinutes > MaxTimeoutMinutes {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "timeout-minutes", Message: fmt.Sprintf("timeout-minutes must be between 1 and %d", MaxTimeoutMinutes)})
	}
	if step.WorkingDirectory != "" && !validRelativePath(step.WorkingDirectory) {
		*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "working-directory", Message: "working-directory must be a relative path inside the repository"})
	}
	for name, value := range step.Env {
		if !secretRefPattern.MatchString(name) {
			*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "env", Message: "invalid environment variable name: " + name})
		}
		if strings.Contains(value, "${{") {
			*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "env", Message: "expressions are not supported in v1"})
		}
	}
	if step.Builtin == BuiltinCheckout && len(step.With) > 0 {
		*issues = append(*issues, Issue{Severity: SeverityWarning, Path: prefix + "with", Message: "checkout takes no inputs; the exact revision is always used"})
	}
	if step.Builtin == BuiltinUploadArtifact {
		name := strings.TrimSpace(step.With["name"])
		target := strings.TrimSpace(step.With["path"])
		if name == "" || target == "" {
			*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "with", Message: "upload-artifact needs name and path"})
		} else {
			if !validArtifactName(name) {
				*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "with.name", Message: "artifact name must be 1-128 characters without path separators"})
			}
			if !validRelativePath(target) {
				*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "with.path", Message: "artifact path must be relative to the workspace"})
			}
		}
	}
	if len(step.With) > 0 && step.Builtin != BuiltinUploadArtifact {
		for key := range step.With {
			*issues = append(*issues, Issue{Severity: SeverityError, Path: prefix + "with", Message: "unsupported input: " + key})
		}
	}
	return result
}

func hasCycle(jobs []Job) bool {
	state := make(map[string]int, len(jobs))
	var visit func(string) bool
	byID := make(map[string]Job, len(jobs))
	for _, job := range jobs {
		byID[job.ID] = job
	}
	visit = func(id string) bool {
		switch state[id] {
		case 1:
			return true
		case 2:
			return false
		}
		state[id] = 1
		for _, need := range byID[id].Needs {
			if visit(need) {
				return true
			}
		}
		state[id] = 2
		return false
	}
	for _, job := range jobs {
		if visit(job.ID) {
			return true
		}
	}
	return false
}

// validBranchPattern accepts an exact branch name or a simple trailing-* glob
// such as "release/*", which is the whole pattern language v1 supports for push
// filters. The glob is only a prefix match, so it can never widen a workflow to
// branches outside the named prefix.
func validBranchPattern(pattern string) bool {
	if pattern == "" || len(pattern) > 255 || strings.Count(pattern, "*") > 1 {
		return false
	}
	base := pattern
	if star := strings.IndexByte(pattern, '*'); star >= 0 {
		// A star anywhere but the end would need full glob semantics.
		if star != len(pattern)-1 {
			return false
		}
		base = strings.TrimSuffix(pattern, "*")
	}
	if base == "" || strings.HasPrefix(base, "/") || strings.Contains(base, "..") {
		return false
	}
	for _, char := range base {
		if char < 0x20 || char == 0x7f || strings.ContainsRune(" ~^:?[\\", char) {
			return false
		}
	}
	return true
}

// validRelativePath rejects absolute paths, traversal, and control characters.
// It is shared by working-directory and artifact paths; the runner re-checks it
// against the real filesystem before use.
func validRelativePath(candidate string) bool {
	if candidate == "" || len(candidate) > 1024 {
		return false
	}
	if strings.HasPrefix(candidate, "/") || strings.Contains(candidate, "\\") {
		return false
	}
	cleaned := path.Clean(candidate)
	if cleaned != candidate || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return false
	}
	for _, char := range candidate {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validArtifactName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return false
	}
	for _, char := range name {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}
