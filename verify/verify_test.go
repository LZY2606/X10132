package verify

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	scriptName     = "verify.sh"
	exemptionsName = "cross_exemptions.txt"
	manifestName   = "verify-manifest.json"
)

var expectedStages = []string{"fmt", "vet", "build", "test", "test-race", "cross"}

var requiredTargets = []string{"linux/amd64", "darwin/amd64", "freebsd/amd64", "windows/amd64"}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

func scriptPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), scriptName)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// scriptCrossTargets extracts the VERIFY_CROSS_TARGETS list from verify.sh.
func scriptCrossTargets(t *testing.T) []string {
	t.Helper()
	script := readFile(t, scriptPath(t))
	re := regexp.MustCompile(`(?m)^VERIFY_CROSS_TARGETS="([^"]+)"`)
	m := re.FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("%s: VERIFY_CROSS_TARGETS definition not found", scriptName)
	}
	targets := strings.Fields(m[1])
	if len(targets) == 0 {
		t.Fatalf("%s: VERIFY_CROSS_TARGETS is empty", scriptName)
	}
	return targets
}

// repoPackages returns the sorted module package list.
func repoPackages(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list ./...: %v", err)
	}
	pkgs := strings.Fields(string(out))
	sort.Strings(pkgs)
	if len(pkgs) == 0 {
		t.Fatal("go list ./... returned no packages")
	}
	return pkgs
}

// runVerify executes verify.sh from an unrelated working directory with the
// given stages and returns the manifest bytes.
func runVerify(t *testing.T, stages string) []byte {
	t.Helper()
	tmp := t.TempDir()
	artifacts := filepath.Join(tmp, "artifacts")
	cmd := exec.Command("bash", scriptPath(t))
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(),
		"VERIFY_STAGES="+stages,
		"VERIFY_ARTIFACTS_DIR="+artifacts,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("verify.sh failed: %v\n%s", err, out)
	}
	manifest, err := os.ReadFile(filepath.Join(artifacts, manifestName))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	return manifest
}

type manifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	GoVersion     string `json:"goVersion"`
	Module        string `json:"module"`
	Platform      struct {
		GOOS   string `json:"goos"`
		GOARCH string `json:"goarch"`
	} `json:"platform"`
	Packages []string `json:"packages"`
	Stages   []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Log    string `json:"log"`
	} `json:"stages"`
	CrossTargets []struct {
		Target     string   `json:"target"`
		GOOS       string   `json:"goos"`
		GOARCH     string   `json:"goarch"`
		Status     string   `json:"status"`
		Log        string   `json:"log"`
		Packages   []string `json:"packages"`
		Exemptions []string `json:"exemptions"`
	} `json:"crossTargets"`
	Result string `json:"result"`
}

func decodeManifest(t *testing.T, data []byte) manifest {
	t.Helper()
	var m manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("manifest does not match expected schema: %v", err)
	}
	return m
}

// walkJSONStrings visits every key and string value in a decoded JSON value.
func walkJSONStrings(v interface{}, fn func(key, value string)) {
	switch node := v.(type) {
	case map[string]interface{}:
		for k, child := range node {
			fn(k, "")
			walkJSONStrings(child, fn)
		}
	case []interface{}:
		for _, child := range node {
			walkJSONStrings(child, fn)
		}
	case string:
		fn("", node)
	}
}

func TestVerifyScriptSyntax(t *testing.T) {
	cmd := exec.Command("bash", "-n", scriptPath(t))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n %s: %v\n%s", scriptName, err, out)
	}
}

func TestManifestSchema(t *testing.T) {
	data := runVerify(t, "fmt cross")

	// The manifest must be deterministic: identical runs, identical bytes.
	if again := runVerify(t, "fmt cross"); !bytes.Equal(data, again) {
		t.Fatal("manifest is not stable across identical runs")
	}

	m := decodeManifest(t, data)

	if m.SchemaVersion != 1 {
		t.Errorf("schemaVersion = %d, want 1", m.SchemaVersion)
	}
	if !regexp.MustCompile(`^go[0-9]+\.[0-9]+`).MatchString(m.GoVersion) {
		t.Errorf("unexpected goVersion %q", m.GoVersion)
	}
	if m.Module != "github.com/lesismal/nbio" {
		t.Errorf("module = %q", m.Module)
	}
	if m.Platform.GOOS == "" || m.Platform.GOARCH == "" {
		t.Errorf("platform not recorded: %+v", m.Platform)
	}
	if m.Result != "passed" {
		t.Errorf("result = %q, want passed", m.Result)
	}

	pkgs := repoPackages(t)
	if strings.Join(m.Packages, " ") != strings.Join(pkgs, " ") {
		t.Errorf("manifest packages differ from go list ./...\n got: %v\nwant: %v", m.Packages, pkgs)
	}
	if !sort.StringsAreSorted(m.Packages) {
		t.Errorf("packages not sorted")
	}

	if len(m.Stages) != len(expectedStages) {
		t.Fatalf("stages = %v, want %v", stageNames(m), expectedStages)
	}
	for i, want := range expectedStages {
		if m.Stages[i].Name != want {
			t.Fatalf("stage order = %v, want %v", stageNames(m), expectedStages)
		}
	}
	validStatus := map[string]bool{"passed": true, "failed": true, "skipped": true}
	for _, st := range m.Stages {
		if !validStatus[st.Status] {
			t.Errorf("stage %s: invalid status %q", st.Name, st.Status)
		}
		if st.Log == "" || filepath.IsAbs(st.Log) {
			t.Errorf("stage %s: log path %q must be relative", st.Name, st.Log)
		}
	}
	// Ran with VERIFY_STAGES="fmt cross".
	for _, st := range m.Stages {
		want := "skipped"
		if st.Name == "fmt" || st.Name == "cross" {
			want = "passed"
		}
		if st.Status != want {
			t.Errorf("stage %s status = %q, want %q", st.Name, st.Status, want)
		}
	}

	targets := scriptCrossTargets(t)
	if len(m.CrossTargets) != len(targets) {
		t.Fatalf("crossTargets = %d, want %d", len(m.CrossTargets), len(targets))
	}
	for i, ct := range m.CrossTargets {
		if ct.Target != targets[i] {
			t.Fatalf("crossTargets order = %v, want %v", crossTargetNames(m), targets)
		}
		parts := strings.Split(ct.Target, "/")
		if ct.GOOS != parts[0] || ct.GOARCH != parts[1] {
			t.Errorf("target %s: goos/goarch fields inconsistent", ct.Target)
		}
		if ct.Status != "passed" {
			t.Errorf("target %s status = %q, want passed", ct.Target, ct.Status)
		}
		if filepath.IsAbs(ct.Log) {
			t.Errorf("target %s: log path %q must be relative", ct.Target, ct.Log)
		}
		if strings.Join(ct.Packages, " ") != strings.Join(pkgs, " ") {
			t.Errorf("target %s: packages differ from go list ./...", ct.Target)
		}
		if !sort.StringsAreSorted(ct.Packages) {
			t.Errorf("target %s: packages not sorted", ct.Target)
		}
		if ct.Exemptions == nil {
			t.Errorf("target %s: exemptions must be an array, not null", ct.Target)
		}
	}

	// The manifest must not embed absolute paths or timestamps.
	var generic interface{}
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	walkJSONStrings(generic, func(key, value string) {
		if key != "" {
			lower := strings.ToLower(key)
			for _, bad := range []string{"time", "date"} {
				if strings.Contains(lower, bad) {
					t.Errorf("manifest key %q looks like a timestamp field", key)
				}
			}
			return
		}
		if strings.HasPrefix(value, "/") {
			t.Errorf("manifest contains absolute path %q", value)
		}
	})
}

func stageNames(m manifest) []string {
	names := make([]string, 0, len(m.Stages))
	for _, s := range m.Stages {
		names = append(names, s.Name)
	}
	return names
}

func crossTargetNames(m manifest) []string {
	names := make([]string, 0, len(m.CrossTargets))
	for _, ct := range m.CrossTargets {
		names = append(names, ct.Target)
	}
	return names
}

func TestCrossExemptionsValid(t *testing.T) {
	path := filepath.Join(repoRoot(t), "verify", exemptionsName)
	content := readFile(t, path)

	targets := make(map[string]bool)
	for _, target := range scriptCrossTargets(t) {
		targets[target] = true
	}
	pkgs := make(map[string]bool)
	for _, pkg := range repoPackages(t) {
		pkgs[pkg] = true
	}

	seen := make(map[string]bool)
	entries := 0
	for lineno, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries++
		fields := strings.Fields(line)
		if len(fields) < 3 {
			t.Errorf("%s:%d: exemption needs '<goos>/<goarch> <package> <reason>'", path, lineno+1)
			continue
		}
		target, pkg, reason := fields[0], fields[1], fields[2:]
		if !targets[target] {
			t.Errorf("%s:%d: target %q is not in VERIFY_CROSS_TARGETS", path, lineno+1, target)
		}
		if !pkgs[pkg] {
			t.Errorf("%s:%d: package %q is not a real package in this module", path, lineno+1, pkg)
		}
		if len(reason) == 0 {
			t.Errorf("%s:%d: exemption for %s/%s must state a reason", path, lineno+1, target, pkg)
		}
		key := target + " " + pkg
		if seen[key] {
			t.Errorf("%s:%d: duplicate exemption %q", path, lineno+1, key)
		}
		seen[key] = true
	}
	t.Logf("%d exemption entries validated", entries)
}

// TestPlatformFilesCovered ensures every platform-specific Go source file in
// the repository is exercised by at least one verify.sh cross target, so a
// newly added platform file cannot silently escape the gate.
func TestPlatformFilesCovered(t *testing.T) {
	goosSet, goarchSet := distList(t)

	targetGOOS := make(map[string]bool)
	targets := scriptCrossTargets(t)
	for _, target := range targets {
		parts := strings.Split(target, "/")
		if len(parts) != 2 {
			t.Fatalf("malformed cross target %q", target)
		}
		targetGOOS[parts[0]] = true
	}
	for _, want := range requiredTargets {
		found := false
		for _, target := range targets {
			if target == want {
				found = true
			}
		}
		if !found {
			t.Errorf("required cross target %s missing from VERIFY_CROSS_TARGETS", want)
		}
	}

	root := repoRoot(t)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "artifacts" || (strings.HasPrefix(base, ".") && path != root) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fileGOOS := platformFileGOOS(t, path, goosSet, goarchSet)
		if len(fileGOOS) == 0 {
			return nil
		}
		covered := false
		for goos := range fileGOOS {
			if targetGOOS[goos] {
				covered = true
			}
		}
		if !covered {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("platform file %s (GOOS=%v) is not covered by any verify.sh cross target", rel, sortedKeys(fileGOOS))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
}

func distList(t *testing.T) (goosSet, goarchSet map[string]bool) {
	t.Helper()
	out, err := exec.Command("go", "tool", "dist", "list").Output()
	if err != nil {
		t.Fatalf("go tool dist list: %v", err)
	}
	goosSet = make(map[string]bool)
	goarchSet = make(map[string]bool)
	for _, line := range strings.Fields(string(out)) {
		parts := strings.Split(line, "/")
		if len(parts) == 2 {
			goosSet[parts[0]] = true
			goarchSet[parts[1]] = true
		}
	}
	return goosSet, goarchSet
}

// platformFileGOOS returns the set of GOOS values a source file is restricted
// to, derived from its filename suffix and its build constraints. Files
// without platform restrictions return an empty set.
func platformFileGOOS(t *testing.T, path string, goosSet, goarchSet map[string]bool) map[string]bool {
	t.Helper()
	found := make(map[string]bool)

	base := strings.TrimSuffix(filepath.Base(path), ".go")
	for _, token := range strings.Split(base, "_")[1:] {
		if goosSet[token] {
			found[token] = true
		}
	}

	content := readFile(t, path)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			break
		}
		var expr string
		switch {
		case strings.HasPrefix(trimmed, "//go:build"):
			expr = strings.TrimSpace(strings.TrimPrefix(trimmed, "//go:build"))
		case strings.HasPrefix(trimmed, "// +build"):
			expr = strings.TrimSpace(strings.TrimPrefix(trimmed, "// +build"))
			expr = strings.ReplaceAll(expr, ",", " ")
		default:
			continue
		}
		for _, token := range strings.Fields(expr) {
			if strings.HasPrefix(token, "!") {
				continue
			}
			token = strings.Trim(token, "()")
			if goosSet[token] {
				found[token] = true
			}
		}
	}
	return found
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
