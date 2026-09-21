package nbio

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// verifyTargets parses the VERIFY_TARGETS declaration from verify.sh.
func verifyTargets(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("verify.sh")
	if err != nil {
		t.Fatalf("read verify.sh: %v", err)
	}
	re := regexp.MustCompile(`(?m)^VERIFY_TARGETS="([^"]+)"`)
	m := re.FindSubmatch(data)
	if m == nil {
		t.Fatalf("verify.sh does not declare VERIFY_TARGETS")
	}
	targets := strings.Fields(string(m[1]))
	if len(targets) == 0 {
		t.Fatalf("verify.sh declares no VERIFY_TARGETS")
	}
	for _, target := range targets {
		parts := strings.Split(target, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			t.Fatalf("invalid target %q in verify.sh VERIFY_TARGETS", target)
		}
	}
	return targets
}

func verifyTargetGOOS(t *testing.T) map[string]bool {
	t.Helper()
	set := map[string]bool{}
	for _, target := range verifyTargets(t) {
		set[strings.SplitN(target, "/", 2)[0]] = true
	}
	return set
}

func goListPackages(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "./...").Output()
	if err != nil {
		t.Fatalf("go list ./...: %v", err)
	}
	return strings.Fields(string(out))
}

// knownGOOS returns the GOOS set supported by the current toolchain.
func knownGOOS(t *testing.T) map[string]bool {
	t.Helper()
	out, err := exec.Command("go", "tool", "dist", "list").Output()
	if err != nil {
		t.Fatalf("go tool dist list: %v", err)
	}
	set := map[string]bool{}
	for _, line := range strings.Fields(string(out)) {
		set[strings.SplitN(line, "/", 2)[0]] = true
	}
	return set
}

type verifyExemption struct {
	Package string   `json:"package"`
	Targets []string `json:"targets"`
	Reason  string   `json:"reason"`
}

type verifyExemptionFile struct {
	Exemptions []verifyExemption `json:"exemptions"`
}

// TestVerifyExemptions makes sure every cross-compilation exemption still
// names a real package of this module and a target covered by verify.sh.
func TestVerifyExemptions(t *testing.T) {
	data, err := os.ReadFile("verify-exemptions.json")
	if err != nil {
		t.Fatalf("read verify-exemptions.json: %v", err)
	}
	var ef verifyExemptionFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ef); err != nil {
		t.Fatalf("parse verify-exemptions.json: %v", err)
	}

	targets := map[string]bool{}
	for _, target := range verifyTargets(t) {
		targets[target] = true
	}
	pkgs := map[string]bool{}
	for _, pkg := range goListPackages(t) {
		pkgs[pkg] = true
	}

	for i, e := range ef.Exemptions {
		if e.Package == "" {
			t.Errorf("exemption %d: empty package", i)
			continue
		}
		if !pkgs[e.Package] {
			t.Errorf("exemption %d: package %q is not a package of this module", i, e.Package)
		}
		if e.Reason == "" {
			t.Errorf("exemption %d (%v): empty reason", i, e.Package)
		}
		if len(e.Targets) == 0 {
			t.Errorf("exemption %d (%v): empty targets", i, e.Package)
		}
		for _, target := range e.Targets {
			if !targets[target] {
				t.Errorf("exemption %d (%v): target %q is not in verify.sh VERIFY_TARGETS", i, e.Package, target)
			}
		}
	}
}

type verifyManifest struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Module        string              `json:"module"`
	GoVersion     string              `json:"goVersion"`
	Platform      string              `json:"platform"`
	Stages        []verifyManifestStg `json:"stages"`
	Result        string              `json:"result"`
}

type verifyManifestStg struct {
	Name       string                  `json:"name"`
	Result     string                  `json:"result"`
	Files      []string                `json:"files,omitempty"`
	Packages   []string                `json:"packages,omitempty"`
	Exemptions []verifyManifestExemptn `json:"exemptions,omitempty"`
	Targets    []verifyManifestTarget  `json:"targets,omitempty"`
}

type verifyManifestExemptn struct {
	Target  string `json:"target"`
	Package string `json:"package"`
	Reason  string `json:"reason"`
}

type verifyManifestTarget struct {
	Target   string   `json:"target"`
	Result   string   `json:"result"`
	Packages []string `json:"packages"`
	Exempted []string `json:"exempted"`
}

// TestVerifyManifestSchema validates the manifest produced by the most recent
// ./verify.sh run. It is skipped when no manifest exists yet.
func TestVerifyManifestSchema(t *testing.T) {
	const manifestPath = "artifacts/verify-manifest.json"
	data, err := os.ReadFile(manifestPath)
	if os.IsNotExist(err) {
		t.Skip("no artifacts/verify-manifest.json; run ./verify.sh first")
	}
	if err != nil {
		t.Fatalf("read %v: %v", manifestPath, err)
	}

	// The manifest must not embed absolute paths of the checkout.
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if bytes.Contains(data, []byte(root)) {
		t.Errorf("manifest contains the absolute checkout path %q", root)
	}

	var m verifyManifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("parse %v: %v", manifestPath, err)
	}

	if m.SchemaVersion != 1 {
		t.Errorf("schemaVersion = %v, want 1", m.SchemaVersion)
	}
	if m.Module == "" {
		t.Errorf("empty module")
	}
	if !strings.HasPrefix(m.GoVersion, "go") || strings.ContainsAny(m.GoVersion, " \t\n") {
		t.Errorf("invalid goVersion %q", m.GoVersion)
	}
	if m.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("platform = %q, want %q", m.Platform, runtime.GOOS+"/"+runtime.GOARCH)
	}
	if m.Result != "pass" && m.Result != "fail" {
		t.Errorf("invalid result %q", m.Result)
	}

	wantStages := []string{"gofmt", "vet", "test", "test-race", "build", "exemptions", "cross-build"}
	if len(m.Stages) != len(wantStages) {
		t.Fatalf("stages = %d, want %d (%v)", len(m.Stages), len(wantStages), wantStages)
	}
	anyFail := false
	for i, st := range m.Stages {
		if st.Name != wantStages[i] {
			t.Errorf("stage %d = %q, want %q (order must be stable)", i, st.Name, wantStages[i])
		}
		if st.Result != "pass" && st.Result != "fail" {
			t.Errorf("stage %q: invalid result %q", st.Name, st.Result)
		}
		if st.Result == "fail" {
			anyFail = true
		}
		if !sort.StringsAreSorted(st.Packages) {
			t.Errorf("stage %q: packages not sorted", st.Name)
		}
		for _, pkg := range st.Packages {
			if filepath.IsAbs(pkg) || strings.Contains(pkg, "\\") {
				t.Errorf("stage %q: package %q is not a relative import path", st.Name, pkg)
			}
		}
		switch st.Name {
		case "vet", "test", "test-race", "build":
			if len(st.Packages) == 0 {
				t.Errorf("stage %q: empty package set", st.Name)
			}
		case "cross-build":
			targets := verifyTargets(t)
			if len(st.Targets) != len(targets) {
				t.Errorf("cross-build: %d targets, want %d", len(st.Targets), len(targets))
			}
			for j, tg := range st.Targets {
				if j < len(targets) && tg.Target != targets[j] {
					t.Errorf("cross-build target %d = %q, want %q", j, tg.Target, targets[j])
				}
				if tg.Result != "pass" && tg.Result != "fail" {
					t.Errorf("cross-build target %q: invalid result %q", tg.Target, tg.Result)
				}
				if !sort.StringsAreSorted(tg.Packages) {
					t.Errorf("cross-build target %q: packages not sorted", tg.Target)
				}
				if len(tg.Packages) == 0 {
					t.Errorf("cross-build target %q: empty package set", tg.Target)
				}
			}
		}
	}
	if anyFail && m.Result != "fail" {
		t.Errorf("result = %q but at least one stage failed", m.Result)
	}
	if !anyFail && m.Result != "pass" {
		t.Errorf("result = %q but all stages passed", m.Result)
	}
}

// TestVerifyPlatformCoverage makes sure verify.sh keeps compiling for every
// platform that has platform-specific files in this repository: a new
// GOOS-suffixed file (e.g. foo_netbsd.go) or a GOOS-only build constraint
// that is not covered by VERIFY_TARGETS fails this test.
func TestVerifyPlatformCoverage(t *testing.T) {
	info, err := os.Stat("verify.sh")
	if err != nil {
		t.Fatalf("stat verify.sh: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("verify.sh is not executable")
	}

	targetGOOS := verifyTargetGOOS(t)
	for _, goos := range []string{"linux", "darwin", "freebsd", "windows"} {
		if !targetGOOS[goos] {
			t.Errorf("verify.sh VERIFY_TARGETS does not cover required platform %q", goos)
		}
	}
	goosSet := knownGOOS(t)

	err = filepath.Walk(".", func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			switch fi.Name() {
			case ".git", "artifacts":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		// GOOS implied by the file name suffix, e.g. poller_epoll_linux.go.
		base := strings.TrimSuffix(filepath.Base(path), ".go")
		base = strings.TrimSuffix(base, "_test")
		if idx := strings.LastIndex(base, "_"); idx >= 0 {
			if suffix := base[idx+1:]; goosSet[suffix] && !targetGOOS[suffix] {
				t.Errorf("%v: platform file for %q is not covered by verify.sh VERIFY_TARGETS", path, suffix)
			}
		}

		// GOOS terms in //go:build constraints must overlap VERIFY_TARGETS.
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "//go:build ") {
				continue
			}
			expr := strings.TrimPrefix(line, "//go:build ")
			mentioned := map[string]bool{}
			for _, tok := range strings.FieldsFunc(expr, func(r rune) bool {
				return r == ' ' || r == '\t' || r == '(' || r == ')' || r == '!'
			}) {
				tok = strings.TrimSpace(tok)
				if goosSet[tok] {
					mentioned[tok] = true
				}
			}
			if len(mentioned) == 0 {
				continue
			}
			covered := false
			for goos := range mentioned {
				if targetGOOS[goos] {
					covered = true
					break
				}
			}
			if !covered {
				t.Errorf("%v: build constraint %q mentions only platforms not covered by verify.sh VERIFY_TARGETS", path, expr)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
