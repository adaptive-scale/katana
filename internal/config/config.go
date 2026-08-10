// Package config loads and validates the katana configuration file.
package config

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FileName is the default configuration file name, resolved from the project root.
const FileName = "katana.yaml"

// Dir is the directory katana keeps its bookkeeping in, relative to the project root.
const Dir = ".katana"

// Config is the parsed katana.yaml.
type Config struct {
	Version   int        `yaml:"version"`
	Harness   Harness    `yaml:"harness"`
	Defaults  Defaults   `yaml:"defaults"`
	Test      Test       `yaml:"test"`
	Behaviors []Behavior `yaml:"behaviors"`

	// Root is the directory containing katana.yaml. Every relative path in the
	// config is resolved against it, not against the process working directory.
	Root string `yaml:"-"`
}

// Harness describes the coding agent katana shells out to for generation.
type Harness struct {
	// Name selects a built-in harness: claude, codex, opencode, pi or hermes.
	Name string `yaml:"name"`
	// Command overrides the executable for the selected harness.
	Command string `yaml:"command"`
	// Args overrides the arguments placed before the prompt.
	Args []string `yaml:"args"`
	// Prompt selects how the prompt reaches the harness: "stdin" or "arg".
	Prompt string `yaml:"prompt"`
	// Model is passed through with the harness's model flag when set.
	Model string `yaml:"model"`
	// ModelFlag overrides the flag used to pass Model.
	ModelFlag string `yaml:"model_flag"`
	// Timeout bounds a single generation. Accepts Go duration syntax ("10m").
	Timeout string `yaml:"timeout"`
	// Jobs is how many behaviors are generated at once. Zero means katana's
	// default; one is sequential.
	Jobs int `yaml:"jobs"`
	// Env is added to the harness process environment.
	Env map[string]string `yaml:"env"`
}

// Defaults supplies the per-behavior settings that a behavior does not override.
type Defaults struct {
	Language  string `yaml:"language"`
	Framework string `yaml:"framework"`
	OutputDir string `yaml:"output_dir"`
	// OutputTemplate names generated files. Supports {name}, {Name} and {snake}.
	OutputTemplate string `yaml:"output_template"`
}

// Test describes how `katana run` executes the generated suite.
type Test struct {
	Command string `yaml:"command"`
	Dir     string `yaml:"dir"`
}

// Behavior is one behavior source. Path may be a glob.
type Behavior struct {
	Path      string `yaml:"path"`
	Output    string `yaml:"output"`
	Language  string `yaml:"language"`
	Framework string `yaml:"framework"`
	Harness   string `yaml:"harness"`
	// Instructions are appended to the generation prompt for this behavior only.
	Instructions string `yaml:"instructions"`
}

// Resolved is a single behavior file with every default already applied.
type Resolved struct {
	Source       string // path to the behavior markdown, relative to Root
	Output       string // path to the generated test file, relative to Root
	Language     string
	Framework    string
	Harness      string
	Instructions string
}

// AbsSource returns the absolute path of the behavior file.
func (r Resolved) AbsSource(root string) string { return filepath.Join(root, r.Source) }

// AbsOutput returns the absolute path of the generated test file.
func (r Resolved) AbsOutput(root string) string { return filepath.Join(root, r.Output) }

// Find walks up from start looking for katana.yaml, mirroring how git finds .git.
func Find(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, FileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s found in %s or any parent directory (run `katana init` first)", FileName, start)
		}
		dir = parent
	}
}

// Load reads and validates the config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.Root = filepath.Dir(abs)
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Harness.Name == "" {
		c.Harness.Name = "claude"
	}
	if c.Defaults.Language == "" {
		c.Defaults.Language = "go"
	}
	if c.Defaults.OutputDir == "" {
		c.Defaults.OutputDir = "tests"
	}
	if c.Defaults.Framework == "" {
		c.Defaults.Framework = DefaultFramework(c.Defaults.Language)
	}
	if c.Defaults.OutputTemplate == "" {
		c.Defaults.OutputTemplate = DefaultOutputTemplate(c.Defaults.Language)
	}
	if c.Test.Command == "" {
		c.Test.Command = DefaultTestCommand(c.Defaults.Language)
	}
}

func (c *Config) validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d (this katana understands version 1)", c.Version)
	}
	if len(c.Behaviors) == 0 {
		return fmt.Errorf("no behaviors configured in %s", FileName)
	}
	for i, b := range c.Behaviors {
		if strings.TrimSpace(b.Path) == "" {
			return fmt.Errorf("behaviors[%d]: path is required", i)
		}
		if b.Output != "" && strings.ContainsAny(b.Path, "*?[") {
			return fmt.Errorf("behaviors[%d]: output cannot be set for a glob path %q; use output_template or list the files individually", i, b.Path)
		}
	}
	if p := c.Harness.Prompt; p != "" && p != "stdin" && p != "arg" {
		return fmt.Errorf("harness.prompt must be \"stdin\" or \"arg\", got %q", p)
	}
	if c.Harness.Jobs < 0 {
		return fmt.Errorf("harness.jobs must be zero or positive, got %d", c.Harness.Jobs)
	}
	if _, err := c.HarnessTimeout(); err != nil {
		return err
	}
	return nil
}

// HarnessTimeout returns the configured per-generation timeout.
func (c *Config) HarnessTimeout() (time.Duration, error) {
	if strings.TrimSpace(c.Harness.Timeout) == "" {
		return 10 * time.Minute, nil
	}
	d, err := time.ParseDuration(c.Harness.Timeout)
	if err != nil {
		return 0, fmt.Errorf("harness.timeout %q: %w", c.Harness.Timeout, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("harness.timeout must be positive, got %q", c.Harness.Timeout)
	}
	return d, nil
}

// DefaultJobs is how many behaviors katana generates at once when neither
// katana.yaml nor --jobs says otherwise.
//
// Generation waits on an agent CLI rather than on this machine, so the useful
// number is unrelated to the core count. Four is a compromise: a real speedup
// on a repository full of behaviors, without opening so many agent sessions at
// once that a rate limit turns into a wall of failures.
const DefaultJobs = 4

// HarnessJobs returns the configured number of concurrent generations, or
// DefaultJobs when the config leaves it unset.
func (c *Config) HarnessJobs() int {
	if c.Harness.Jobs > 0 {
		return c.Harness.Jobs
	}
	return DefaultJobs
}

// BehaviorDir returns the directory behavior files live in, which is where
// `katana discover` writes what it finds.
//
// It is the fixed part of the first configured behavior path — the directory a
// glob is rooted at, or the parent of a single named file — so a project that
// moved its behaviors elsewhere is discovered into the same place it already
// keeps them.
func (c *Config) BehaviorDir() string {
	for _, b := range c.Behaviors {
		base := c.patternBase(b.Path)
		// patternBase can only tell a file from a directory by looking, so a
		// path naming a behavior file that does not exist yet still reads as a
		// directory. Its extension gives it away.
		if isMarkdown(base) {
			base = path.Dir(base)
		}
		if base != "" && base != "." && base != "/" {
			return base
		}
	}
	return "behaviors"
}

// Resolve expands globs and applies defaults, returning one entry per behavior
// file sorted by source path. Two behaviors writing the same output file is an
// error — it would make regeneration order-dependent.
func (c *Config) Resolve() ([]Resolved, error) {
	var out []Resolved
	seenSource := map[string]bool{}
	seenOutput := map[string]string{}

	for i, b := range c.Behaviors {
		matches, err := c.expand(b.Path)
		if err != nil {
			return nil, fmt.Errorf("behaviors[%d]: %w", i, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("behaviors[%d]: %q matched no files", i, b.Path)
		}
		base := c.patternBase(b.Path)
		for _, src := range matches {
			if seenSource[src] {
				continue // a file caught by two globs is generated once
			}
			seenSource[src] = true

			r := Resolved{
				Source:       src,
				Language:     firstNonEmpty(b.Language, c.Defaults.Language),
				Framework:    b.Framework,
				Harness:      firstNonEmpty(b.Harness, c.Harness.Name),
				Instructions: b.Instructions,
			}
			if r.Framework == "" {
				// Only fall back to the global default when the language matches it,
				// otherwise a per-behavior language would inherit a mismatched framework.
				if r.Language == c.Defaults.Language {
					r.Framework = c.Defaults.Framework
				} else {
					r.Framework = DefaultFramework(r.Language)
				}
			}
			if b.Output != "" {
				r.Output = filepath.ToSlash(filepath.Clean(b.Output))
			} else {
				tmpl := c.Defaults.OutputTemplate
				if r.Language != c.Defaults.Language {
					tmpl = DefaultOutputTemplate(r.Language)
				}
				// Behaviors in subfolders keep that structure under output_dir, so
				// billing/limits.md and auth/limits.md stay distinct files.
				r.Output = path.Join(c.Defaults.OutputDir, subDir(base, src), renderTemplate(tmpl, src))
			}
			if prev, ok := seenOutput[r.Output]; ok {
				return nil, fmt.Errorf("behaviors %q and %q both generate %q; give one an explicit output", prev, src, r.Output)
			}
			seenOutput[r.Output] = src
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

// expand resolves a path, directory or glob to project-relative slash paths.
// A directory is searched recursively, so behaviors can be grouped into
// subfolders. A glob may use "**" to match any number of path segments.
func (c *Config) expand(pattern string) ([]string, error) {
	full := filepath.Join(c.Root, filepath.FromSlash(pattern))

	var matches []string
	switch {
	case !strings.ContainsAny(pattern, "*?["):
		info, err := os.Stat(full)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", pattern, err)
		}
		if !info.IsDir() {
			return []string{filepath.ToSlash(filepath.Clean(pattern))}, nil
		}
		matches, err = walkMatch(full, func(rel string) (bool, error) {
			return isMarkdown(rel), nil
		})
		if err != nil {
			return nil, fmt.Errorf("%q: %w", pattern, err)
		}
	case strings.Contains(pattern, "**"):
		var err error
		matches, err = c.globStar(pattern)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", pattern, err)
		}
	default:
		var err error
		matches, err = filepath.Glob(full)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", pattern, err)
		}
	}

	var rel []string
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil || info.IsDir() {
			continue
		}
		r, err := filepath.Rel(c.Root, m)
		if err != nil {
			return nil, err
		}
		rel = append(rel, filepath.ToSlash(r))
	}
	sort.Strings(rel)
	return rel, nil
}

// patternBase returns the directory a behavior path is rooted at: everything
// before the first wildcard, or the parent of a single named file. Matched
// files keep their position relative to it when their output path is built.
func (c *Config) patternBase(pattern string) string {
	segments := strings.Split(path.Clean(filepath.ToSlash(pattern)), "/")
	var fixed []string
	for _, s := range segments {
		if strings.ContainsAny(s, "*?[") {
			break
		}
		fixed = append(fixed, s)
	}
	base := path.Join(fixed...)
	if len(fixed) == len(segments) {
		// No wildcard: a directory roots itself, a file is rooted at its parent.
		if info, err := os.Stat(filepath.Join(c.Root, filepath.FromSlash(base))); err == nil && !info.IsDir() {
			base = path.Dir(base)
		}
	}
	return base
}

// subDir returns the part of src's directory that sits below base, or "" when
// src lives directly in base.
func subDir(base, src string) string {
	dir := path.Dir(src)
	switch {
	case base == "" || base == ".":
		if dir == "." {
			return ""
		}
		return dir
	case dir == base:
		return ""
	case strings.HasPrefix(dir, base+"/"):
		return strings.TrimPrefix(dir, base+"/")
	default:
		return ""
	}
}

// globStar expands a pattern containing "**" by walking the fixed prefix of the
// pattern and matching every file beneath it.
func (c *Config) globStar(pattern string) ([]string, error) {
	segments := strings.Split(path.Clean(pattern), "/")
	base := filepath.Join(c.Root, filepath.FromSlash(c.patternBase(pattern)))
	if _, err := os.Stat(base); err != nil {
		if os.IsNotExist(err) {
			return nil, nil // an absent directory is "no matches", as with Glob
		}
		return nil, err
	}
	return walkMatch(base, func(abs string) (bool, error) {
		r, err := filepath.Rel(c.Root, abs)
		if err != nil {
			return false, err
		}
		return matchSegments(segments, strings.Split(filepath.ToSlash(r), "/"))
	})
}

// walkMatch returns every file under dir that keep reports as a match, as
// absolute paths. Hidden directories are skipped so tool state (.git, .katana)
// never becomes a behavior.
func walkMatch(dir string, keep func(abs string) (bool, error)) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		ok, err := keep(p)
		if err != nil {
			return err
		}
		if ok {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// matchSegments matches slash-separated path segments against pattern segments,
// where "**" stands for zero or more segments. Every other segment is matched
// with path.Match, so "*" stays confined to one segment.
func matchSegments(pattern, name []string) (bool, error) {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			for i := 0; i <= len(name); i++ {
				ok, err := matchSegments(pattern[1:], name[i:])
				if ok || err != nil {
					return ok, err
				}
			}
			return false, nil
		}
		if len(name) == 0 {
			return false, nil
		}
		ok, err := path.Match(pattern[0], name[0])
		if !ok || err != nil {
			return false, err
		}
		pattern, name = pattern[1:], name[1:]
	}
	return len(name) == 0, nil
}

func isMarkdown(p string) bool {
	return strings.EqualFold(filepath.Ext(p), ".md")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
