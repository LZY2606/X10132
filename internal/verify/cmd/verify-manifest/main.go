// Command verify-manifest assembles artifacts/verify-manifest.json from the
// stage records written by verify.sh. It lives in the repo so the manifest
// schema is testable and the script stays free of hand-rolled JSON.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lesismal/nbio/internal/verify"
)

func main() {
	var (
		goVersion = flag.String("go-version", "", "go toolchain version, e.g. go1.26.4")
		records   = flag.String("records", "", "path to the tab-separated stage records file")
		out       = flag.String("out", "", "path of the manifest to write")
	)
	flag.Parse()
	if *goVersion == "" || *records == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: verify-manifest -go-version V -records F -out F")
		os.Exit(2)
	}

	f, err := os.Open(*records)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open records: %v\n", err)
		os.Exit(1)
	}
	stages, err := verify.ParseRecords(f)
	_ = f.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse records: %v\n", err)
		os.Exit(1)
	}

	manifest, err := verify.Build(*goVersion, stages)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build manifest: %v\n", err)
		os.Exit(1)
	}

	tmp := *out + ".tmp"
	w, err := os.Create(tmp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create manifest: %v\n", err)
		os.Exit(1)
	}
	if err := verify.Write(w, manifest); err != nil {
		_ = w.Close()
		fmt.Fprintf(os.Stderr, "write manifest: %v\n", err)
		os.Exit(1)
	}
	if err := w.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "close manifest: %v\n", err)
		os.Exit(1)
	}
	if err := os.Rename(tmp, filepath.Clean(*out)); err != nil {
		fmt.Fprintf(os.Stderr, "rename manifest: %v\n", err)
		os.Exit(1)
	}
}
