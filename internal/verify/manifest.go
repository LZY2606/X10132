// Package verify builds the machine-readable manifest emitted by verify.sh.
//
// The manifest is deterministic: keys are emitted in a fixed order, package
// lists are sorted, and it never contains absolute paths or timestamps, so
// repeated runs of verify.sh on the same tree produce identical files.
package verify

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// SchemaVersion is the version of the manifest schema emitted by this package.
const SchemaVersion = 1

// Stage results.
const (
	ResultPass = "pass"
	ResultFail = "fail"
)

// Stage describes the outcome of a single verify.sh stage.
type Stage struct {
	Name       string   `json:"name"`
	Target     string   `json:"target"`
	Packages   []string `json:"packages"`
	Exemptions []string `json:"exemptions"`
	Result     string   `json:"result"`
}

// Manifest is the root document written to artifacts/verify-manifest.json.
type Manifest struct {
	SchemaVersion int     `json:"schemaVersion"`
	GoVersion     string  `json:"goVersion"`
	Stages        []Stage `json:"stages"`
}

// ParseRecords reads stage records in the tab-separated format written by
// verify.sh: name, target, result, comma-separated packages, comma-separated
// exemptions. Empty package or exemption fields mean an empty list.
func ParseRecords(r io.Reader) ([]Stage, error) {
	var stages []Stage
	scanner := bufio.NewScanner(r)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			return nil, fmt.Errorf("record %d: want 5 tab-separated fields, got %d", lineNo, len(fields))
		}
		stages = append(stages, Stage{
			Name:       fields[0],
			Target:     fields[1],
			Result:     fields[2],
			Packages:   splitList(fields[3]),
			Exemptions: splitList(fields[4]),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return stages, nil
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// Build validates the stages and returns a normalized Manifest with sorted
// package and exemption lists.
func Build(goVersion string, stages []Stage) (*Manifest, error) {
	if goVersion == "" {
		return nil, fmt.Errorf("go version must not be empty")
	}
	if len(stages) == 0 {
		return nil, fmt.Errorf("manifest must contain at least one stage")
	}
	normalized := make([]Stage, 0, len(stages))
	for _, stage := range stages {
		if stage.Name == "" {
			return nil, fmt.Errorf("stage name must not be empty")
		}
		if stage.Target == "" {
			return nil, fmt.Errorf("stage %q: target must not be empty", stage.Name)
		}
		if stage.Result != ResultPass && stage.Result != ResultFail {
			return nil, fmt.Errorf("stage %q: invalid result %q", stage.Name, stage.Result)
		}
		for _, pkg := range stage.Packages {
			if isAbsPath(pkg) {
				return nil, fmt.Errorf("stage %q: package %q is an absolute path", stage.Name, pkg)
			}
		}
		packages := append([]string{}, stage.Packages...)
		exemptions := append([]string{}, stage.Exemptions...)
		sort.Strings(packages)
		sort.Strings(exemptions)
		normalized = append(normalized, Stage{
			Name:       stage.Name,
			Target:     stage.Target,
			Packages:   packages,
			Exemptions: exemptions,
			Result:     stage.Result,
		})
	}
	return &Manifest{
		SchemaVersion: SchemaVersion,
		GoVersion:     goVersion,
		Stages:        normalized,
	}, nil
}

func isAbsPath(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, `\`) {
		return true
	}
	return len(s) >= 3 && s[1] == ':' && (s[2] == '/' || s[2] == '\\')
}

// Write serializes the manifest with stable formatting.
func Write(w io.Writer, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}
