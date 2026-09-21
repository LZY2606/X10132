package verify

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func sampleStages() []Stage {
	return []Stage{
		{Name: "gofmt", Target: "host", Packages: []string{"github.com/lesismal/nbio/mempool", "github.com/lesismal/nbio"}, Result: ResultPass},
		{Name: "cross-build", Target: "windows/arm64", Packages: []string{"github.com/lesismal/nbio"}, Exemptions: []string{"github.com/lesismal/nbio/example/cgo"}, Result: ResultFail},
	}
}

func TestManifestSchema(t *testing.T) {
	manifest, err := Build("go1.26.4", sampleStages())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	var buf bytes.Buffer
	if err := Write(&buf, manifest); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}

	wantKeys := []string{"schemaVersion", "goVersion", "stages"}
	assertKeys(t, doc, wantKeys)

	if got := doc["schemaVersion"]; got != float64(SchemaVersion) {
		t.Fatalf("schemaVersion = %v, want %v", got, SchemaVersion)
	}
	if got := doc["goVersion"]; got != "go1.26.4" {
		t.Fatalf("goVersion = %v", got)
	}

	stages, ok := doc["stages"].([]interface{})
	if !ok || len(stages) != 2 {
		t.Fatalf("stages has wrong shape: %v", doc["stages"])
	}
	stageKeys := []string{"name", "target", "packages", "exemptions", "result"}
	for _, raw := range stages {
		stage, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("stage is not an object: %v", raw)
		}
		assertKeys(t, stage, stageKeys)
		result, _ := stage["result"].(string)
		if result != ResultPass && result != ResultFail {
			t.Fatalf("invalid result %q", result)
		}
		packages := toStrings(t, stage["packages"])
		if !sort.StringsAreSorted(packages) {
			t.Fatalf("packages not sorted: %v", packages)
		}
		for _, pkg := range packages {
			if isAbsPath(pkg) {
				t.Fatalf("absolute path in manifest: %q", pkg)
			}
		}
	}
}

func TestManifestDeterministic(t *testing.T) {
	marshal := func() string {
		manifest, err := Build("go1.26.4", sampleStages())
		if err != nil {
			t.Fatalf("Build failed: %v", err)
		}
		var buf bytes.Buffer
		if err := Write(&buf, manifest); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		return buf.String()
	}
	if a, b := marshal(), marshal(); a != b {
		t.Fatalf("manifest output is not deterministic:\n%s\n%s", a, b)
	}
}

func TestParseRecords(t *testing.T) {
	records := "gofmt\thost\tpass\tgithub.com/lesismal/nbio,github.com/lesismal/nbio/lmux\t\n" +
		"cross-build\tlinux/arm64\tfail\t\tgithub.com/lesismal/nbio/cgo\n"
	stages, err := ParseRecords(strings.NewReader(records))
	if err != nil {
		t.Fatalf("ParseRecords failed: %v", err)
	}
	if len(stages) != 2 {
		t.Fatalf("got %d stages, want 2", len(stages))
	}
	if got := stages[0].Packages; len(got) != 2 || got[1] != "github.com/lesismal/nbio/lmux" {
		t.Fatalf("unexpected packages: %v", got)
	}
	if got := stages[1].Packages; len(got) != 0 {
		t.Fatalf("empty package field should give empty list, got %v", got)
	}
	if got := stages[1].Exemptions; len(got) != 1 || got[0] != "github.com/lesismal/nbio/cgo" {
		t.Fatalf("unexpected exemptions: %v", got)
	}

	if _, err := ParseRecords(strings.NewReader("only\ttwo\n")); err == nil {
		t.Fatalf("malformed record should be rejected")
	}
}

func TestBuildRejectsBadInput(t *testing.T) {
	if _, err := Build("", sampleStages()); err == nil {
		t.Fatalf("empty go version should be rejected")
	}
	if _, err := Build("go1.26.4", nil); err == nil {
		t.Fatalf("empty stage list should be rejected")
	}
	bad := []Stage{{Name: "build", Target: "host", Packages: []string{"/abs/path/pkg"}, Result: ResultPass}}
	if _, err := Build("go1.26.4", bad); err == nil {
		t.Fatalf("absolute package path should be rejected")
	}
	badResult := []Stage{{Name: "build", Target: "host", Result: "maybe"}}
	if _, err := Build("go1.26.4", badResult); err == nil {
		t.Fatalf("invalid result should be rejected")
	}
}

func assertKeys(t *testing.T, obj map[string]interface{}, want []string) {
	t.Helper()
	if len(obj) != len(want) {
		t.Fatalf("keys = %v, want %v", keysOf(obj), want)
	}
	for _, k := range want {
		if _, ok := obj[k]; !ok {
			t.Fatalf("missing key %q in %v", k, keysOf(obj))
		}
	}
}

func keysOf(obj map[string]interface{}) []string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func toStrings(t *testing.T, v interface{}) []string {
	t.Helper()
	items, ok := v.([]interface{})
	if !ok {
		t.Fatalf("not a list: %v", v)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("not a string: %v", item)
		}
		out = append(out, s)
	}
	return out
}
