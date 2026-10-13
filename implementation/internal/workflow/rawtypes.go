package workflow

// rawWorkflow mirrors the YAML document. Numbers and booleans are decoded into
// typed fields so a wrongly typed value fails validation instead of being
// coerced, and KnownFields(true) rejects every key that is not declared here.
// Workflow- and job-level `env` are intentionally absent: v1 injects only the
// fixed FITHUB_* variables, so an author who writes `env:` there gets an
// explicit rejection instead of an ignored setting.
type rawWorkflow struct {
	Version int                   `yaml:"version"`
	Name    string                `yaml:"name"`
	On      map[string]rawTrigger `yaml:"on"`
	Jobs    map[string]rawJob     `yaml:"jobs"`
}

type rawTrigger struct {
	Branches []string `yaml:"branches"`
	Tags     []string `yaml:"tags"`
	Paths    []string `yaml:"paths"`
}

type rawJob struct {
	Name           string    `yaml:"name"`
	RunsOn         string    `yaml:"runs-on"`
	TimeoutMinutes int       `yaml:"timeout-minutes"`
	Needs          []string  `yaml:"needs"`
	SecretRefs     []string  `yaml:"secret-refs"`
	Steps          []rawStep `yaml:"steps"`
}

type rawStep struct {
	Name             string            `yaml:"name"`
	Run              string            `yaml:"run"`
	Builtin          string            `yaml:"builtin"`
	Shell            string            `yaml:"shell"`
	With             map[string]string `yaml:"with"`
	Env              map[string]string `yaml:"env"`
	WorkingDirectory string            `yaml:"working-directory"`
	TimeoutMinutes   int               `yaml:"timeout-minutes"`
	Condition        string            `yaml:"condition"`
}
