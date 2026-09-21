// Command verifyexempt parses verify-exemptions.json, the explicit list of
// packages that verify.sh is allowed to skip for specific cross-compilation
// targets (for example packages requiring cgo). It validates the schema and
// prints one tab-separated line per exemption: target<TAB>package<TAB>reason.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

type exemption struct {
	Package string   `json:"package"`
	Targets []string `json:"targets"`
	Reason  string   `json:"reason"`
}

type exemptionFile struct {
	Exemptions []exemption `json:"exemptions"`
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "verifyexempt: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	path := "verify-exemptions.json"
	if len(os.Args) > 2 {
		fatal("usage: verifyexempt [exemptions-file]")
	}
	if len(os.Args) == 2 {
		path = os.Args[1]
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fatal("read %v: %v", path, err)
	}

	var ef exemptionFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ef); err != nil {
		fatal("parse %v: %v", path, err)
	}

	var lines []string
	seen := map[string]bool{}
	for i, e := range ef.Exemptions {
		if e.Package == "" {
			fatal("exemption %d: empty package", i)
		}
		if e.Reason == "" {
			fatal("exemption %d (%v): empty reason", i, e.Package)
		}
		if len(e.Targets) == 0 {
			fatal("exemption %d (%v): empty targets", i, e.Package)
		}
		for _, target := range e.Targets {
			key := target + " " + e.Package
			if seen[key] {
				fatal("duplicate exemption for package %v target %v", e.Package, target)
			}
			seen[key] = true
			lines = append(lines, target+"\t"+e.Package+"\t"+e.Reason)
		}
	}
	sort.Strings(lines)
	for _, line := range lines {
		fmt.Println(line)
	}
}
