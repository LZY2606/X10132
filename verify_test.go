package nbio

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	verifyScript   = "verify.sh"
	verifyConfig   = "verify-exemptions.json"
	manifestRel    = "artifacts/verify-manifest.json"
	canonicalOrder = "gofmt vet build test test-race cross-build"
)

type exemptionsConfig struct {
	Version              int `json:"version"`
	CrossBuildExemptions []struct {
		GOOS    string `json:"goos"`
		Package string `json:"package"`
		Reason  string `json:"reason"`
	} `json:"crossBuildExemptions"`
	PlatformsNotGated []struct {
		GOOS   string `json:"goos"`
		Reason string `json:"reason"`
	} `json:"platformsNotGated"`
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wd, verifyScript)); err != nil {
		t.Fatalf("%s not found next to test: %v", verifyScript, err)
	}
	return wd
}

func readExemptionsConfig(t *testing.T, root string) exemptionsConfig {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, verifyConfig))
	if err != nil {
		t.Fatalf("read %s: %v", verifyConfig, err)
	}
	var cfg exemptionsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse %s: %v", verifyConfig, err)
	}
	return cfg
}

func goToolOutput(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := exec.Command("go", args...).Output()
	if err != nil {
		t.Fatalf("go %s: %v", strings.Join(args, " "), err)
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// knownGOOS returns the set of GOOS values supported by the toolchain.
func knownGOOS(t *testing.T) map[string]bool {
	t.Helper()
	set := map[string]bool{}
	for _, pair := range goToolOutput(t, "tool", "dist", "list") {
		if i := strings.Index(pair, "/"); i > 0 {
			set[pair[:i]] = true
		}
	}
	return set
}

// scriptTargets extracts CROSS_TARGETS from verify.sh.
func scriptTargets(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, verifyScript))
	if err != nil {
		t.Fatalf("read %s: %v", verifyScript, err)
	}
	re := regexp.MustCompile(`(?m)^CROSS_TARGETS=\(([^)]*)\)`)
	m := re.FindSubmatch(data)
	if m == nil {
		t.Fatalf("CROSS_TARGETS not found in %s", verifyScript)
	}
	return strings.Fields(string(m[1]))
}

// TestVerifyExemptionsReferenceRealPackages ensures every exemption in
// verify-exemptions.json still points at an existing package and a GOOS
// that the toolchain recognizes, so the list cannot silently go stale.
func TestVerifyExemptionsReferenceRealPackages(t *testing.T) {
	root := repoRoot(t)
	cfg := readExemptionsConfig(t, root)

	pkgs := map[string]bool{}
	for _, p := range goToolOutput(t, "list", "./...") {
		pkgs[p] = true
	}
	goosSet := knownGOOS(t)
	targets := map[string]bool{}
	for _, target := range scriptTargets(t, root) {
		targets[target] = true
	}

	for _, e := range cfg.CrossBuildExemptions {
		if !pkgs[e.Package] {
			t.Errorf("exemption package %q is not a real package", e.Package)
		}
		if !goosSet[e.GOOS] {
			t.Errorf("exemption goos %q is not a known GOOS", e.GOOS)
		}
		if !targets[e.GOOS] {
			t.Errorf("exemption goos %q is not a cross-build target of %s", e.GOOS, verifyScript)
		}
		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("exemption for %q on %q has no reason", e.Package, e.GOOS)
		}
	}
	for _, p := range cfg.PlatformsNotGated {
		if !goosSet[p.GOOS] {
			t.Errorf("platformsNotGated goos %q is not a known GOOS", p.GOOS)
		}
		if strings.TrimSpace(p.Reason) == "" {
			t.Errorf("platformsNotGated entry %q has no reason", p.GOOS)
		}
	}
}

// TestVerifyScriptCoversPlatformFiles scans the repository for
// GOOS-specific sources (file name suffixes and //go:build constraints)
// and requires every discovered GOOS to be either cross-compiled by
// verify.sh or explicitly listed in verify-exemptions.json. Adding a new
// platform file without updating the gate fails this test.
func TestVerifyScriptCoversPlatformFiles(t *testing.T) {
	root := repoRoot(t)
	cfg := readExemptionsConfig(t, root)
	goosSet := knownGOOS(t)

	covered := map[string]bool{}
	for _, target := range scriptTargets(t, root) {
		covered[target] = true
	}
	for _, p := range cfg.PlatformsNotGated {
		covered[p.GOOS] = true
	}

	discovered := map[string]bool{}
	buildTagRe := regexp.MustCompile(`^//go:build\s+(.+)$`)
	tokenRe := regexp.MustCompile(`[a-z0-9_]+`)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "artifacts":
				return filepath.SkipDir
			}
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		// File name suffix, e.g. poller_epoll_linux.go.
		base := strings.TrimSuffix(name, ".go")
		if i := strings.LastIndex(base, "_"); i >= 0 {
			if token := base[i+1:]; goosSet[token] {
				discovered[token] = true
			}
		}
		// //go:build constraints, e.g. //go:build linux || darwin.
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(data), "\n") {
			m := buildTagRe.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				if !strings.HasPrefix(strings.TrimSpace(line), "//") && strings.TrimSpace(line) != "" {
					break // constraints only appear before the package clause
				}
				continue
			}
			for _, token := range tokenRe.FindAllString(m[1], -1) {
				if goosSet[token] {
					discovered[token] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}

	for goos := range discovered {
		if !covered[goos] {
			t.Errorf("platform %q appears in repository sources but is neither a cross-build target of %s nor listed in %s platformsNotGated", goos, verifyScript, verifyConfig)
		}
	}
	for _, target := range scriptTargets(t, root) {
		if !discovered[target] {
			t.Errorf("cross-build target %q has no platform-specific source file; remove it from %s or add coverage", target, verifyScript)
		}
	}
	for _, p := range cfg.PlatformsNotGated {
		if !discovered[p.GOOS] {
			t.Errorf("platformsNotGated entry %q does not match any platform-specific source file; remove the stale entry", p.GOOS)
		}
	}
}

// copyVerificationFiles copies the files verify.sh needs into a temporary
// directory so the test can run the script without touching the real
// working tree (which may be mid-verification itself).
func copyVerificationFiles(t *testing.T, root, dst string) {
	t.Helper()
	skip := map[string]bool{
		".git": true, "artifacts": true, "test_tmp.file": true,
		"test.unix": true, "coverage": true,
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if skip[rel] || strings.HasSuffix(rel, ".test") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := info.Mode()
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(target, data, mode)
	})
	if err != nil {
		t.Fatalf("copy repo: %v", err)
	}
}

// TestVerifyManifestSchema runs verify.sh in a scratch copy of the
// repository and validates the structure and stability of the generated
// manifest.
func TestVerifyManifestSchema(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := repoRoot(t)

	tmp := t.TempDir()
	copyVerificationFiles(t, root, tmp)

	run := func() []byte {
		t.Helper()
		cmd := exec.Command("bash", verifyScript, "gofmt")
		cmd.Dir = tmp
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("verify.sh gofmt failed: %v\n%s", err, out)
		}
		manifest, err := os.ReadFile(filepath.Join(tmp, manifestRel))
		if err != nil {
			t.Fatalf("read manifest: %v", err)
		}
		return manifest
	}

	first := run()
	second := run()
	if string(first) != string(second) {
		t.Fatalf("manifest is not deterministic:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}

	assertKeys := func(obj map[string]interface{}, ctx string, keys ...string) {
		t.Helper()
		allowed := map[string]bool{}
		for _, k := range keys {
			allowed[k] = true
		}
		for k := range obj {
			if !allowed[k] {
				t.Errorf("%s: unexpected key %q", ctx, k)
			}
			lower := strings.ToLower(k)
			if strings.Contains(lower, "time") || strings.Contains(lower, "date") {
				t.Errorf("%s: key %q looks like a timestamp field", ctx, k)
			}
		}
	}
	assertKeys(doc, "manifest", "version", "goVersion", "host", "stages", "status")

	if v, ok := doc["version"].(float64); !ok || int(v) != 1 {
		t.Errorf("manifest version: got %v, want 1", doc["version"])
	}
	if v, _ := doc["goVersion"].(string); !strings.HasPrefix(v, "go") {
		t.Errorf("manifest goVersion: got %q", v)
	}
	if v, _ := doc["status"].(string); v != "passed" && v != "failed" {
		t.Errorf("manifest status: got %q", v)
	}
	host, _ := doc["host"].(map[string]interface{})
	if host == nil {
		t.Fatal("manifest host missing")
	}
	assertKeys(host, "host", "goos", "goarch")
	for _, k := range []string{"goos", "goarch"} {
		if v, _ := host[k].(string); v == "" {
			t.Errorf("manifest host.%s is empty", k)
		}
	}

	stages, _ := doc["stages"].([]interface{})
	if len(stages) == 0 {
		t.Fatal("manifest stages is empty")
	}
	order := strings.Fields(canonicalOrder)
	rank := map[string]int{}
	for i, name := range order {
		rank[name] = i
	}
	last := -1
	for i, s := range stages {
		stage, _ := s.(map[string]interface{})
		if stage == nil {
			t.Fatalf("stage %d is not an object", i)
		}
		assertKeys(stage, fmt.Sprintf("stage %d", i), "name", "status", "log", "packages", "reason", "targets")
		name, _ := stage["name"].(string)
		r, ok := rank[name]
		if !ok {
			t.Errorf("stage %d: unknown stage name %q", i, name)
			continue
		}
		if r < last {
			t.Errorf("stage %q is out of canonical order", name)
		}
		last = r
		switch status, _ := stage["status"].(string); status {
		case "passed", "failed", "skipped":
		default:
			t.Errorf("stage %q: invalid status %q", name, status)
		}
		if pkgs, ok := stage["packages"]; ok {
			list, _ := pkgs.([]interface{})
			if !sortedStrings(list) {
				t.Errorf("stage %q: packages are not sorted", name)
			}
		}
		if targets, ok := stage["targets"]; ok {
			for j, tv := range targets.([]interface{}) {
				target, _ := tv.(map[string]interface{})
				if target == nil {
					t.Fatalf("stage %q target %d is not an object", name, j)
				}
				assertKeys(target, fmt.Sprintf("target %d", j),
					"goos", "goarch", "status", "packages", "exempt", "log")
			}
		}
	}

	// No absolute paths and no host-specific leakage anywhere in the manifest.
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch vv := v.(type) {
		case map[string]interface{}:
			for _, x := range vv {
				walk(x)
			}
		case []interface{}:
			for _, x := range vv {
				walk(x)
			}
		case string:
			if filepath.IsAbs(vv) {
				t.Errorf("manifest contains absolute path %q", vv)
			}
			if strings.Contains(vv, tmp) || strings.Contains(vv, root) {
				t.Errorf("manifest leaks working directory into %q", vv)
			}
		}
	}
	walk(doc)
}

func sortedStrings(list []interface{}) bool {
	var ss []string
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			return false
		}
		ss = append(ss, s)
	}
	return sort.StringsAreSorted(ss)
}
