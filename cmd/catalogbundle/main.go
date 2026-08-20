// Command catalogbundle builds the signable catalog bundle from the tile tree.
//
// It emits the single JSON document ADR-0006 Decision 2 describes: the whole
// corpus plus the metadata a cluster needs to decide whether to adopt it. The
// detached CMS signature is produced separately, over exactly these bytes.
//
//	catalogbundle -version 42 -o catalog.json
//	catalogbundle -version 42 -source "geekdojo/rasputin-app-catalog@$SHA"
//
// It is a CALLER of the shared validators, never a second implementation, and
// it refuses to write a bundle the control plane would reject — a publisher
// that can emit something the fleet rejects has moved the failure out of CI
// and into a cluster, with a signature making it look authoritative en route.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/geekdojo/rasputin-app-catalog/internal/corpus"
)

func main() {
	var (
		root      = flag.String("root", "tiles", "tile corpus directory")
		version   = flag.Int("version", 0, "monotonic catalog version (required, positive)")
		out       = flag.String("o", "catalog.json", "output path, or - for stdout")
		source    = flag.String("source", "", "provenance string, e.g. owner/repo@sha")
		published = flag.String("published", "", "RFC 3339 publish time (default: SOURCE_DATE_EPOCH, else now)")
	)
	flag.Parse()

	if *version <= 0 {
		fmt.Fprintln(os.Stderr, "catalogbundle: -version must be a positive integer")
		fmt.Fprintln(os.Stderr, "  The catalog version is monotonic (ADR-0006 Decision 5): a cluster refuses a bundle")
		fmt.Fprintln(os.Stderr, "  that does not exceed the one it holds, so there is no sensible default.")
		os.Exit(2)
	}

	at, err := publishTime(*published, os.Getenv("SOURCE_DATE_EPOCH"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalogbundle:", err)
		os.Exit(2)
	}

	bundle, err := corpus.BuildBundle(*root, *version, at, *source)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalogbundle:", err)
		os.Exit(1)
	}

	// Indented, not compact. The artifact is signed over its bytes either way,
	// and a catalog someone can read in a browser during an incident is worth
	// more than the few kilobytes compaction saves on a daily poll.
	body, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalogbundle:", err)
		os.Exit(1)
	}
	body = append(body, '\n')

	if *out == "-" {
		if _, err := os.Stdout.Write(body); err != nil {
			fmt.Fprintln(os.Stderr, "catalogbundle:", err)
			os.Exit(1)
		}
	} else if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "catalogbundle:", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "catalog v%d — %d tile(s), %d bytes\n", bundle.Version, len(bundle.Tiles), len(body))
}

// publishTime resolves the timestamp stamped into the bundle.
//
// SOURCE_DATE_EPOCH is honoured so a rebuild of the same commit produces
// byte-identical output — the reproducible-builds convention. Without it a
// rebuild differs in exactly one field, which is enough to make "is this the
// artifact we reviewed?" unanswerable by comparing hashes.
func publishTime(flagVal, epoch string) (string, error) {
	if flagVal != "" {
		t, err := time.Parse(time.RFC3339, flagVal)
		if err != nil {
			return "", fmt.Errorf("-published must be RFC 3339: %w", err)
		}
		return t.UTC().Format(time.RFC3339), nil
	}
	if epoch != "" {
		secs, err := strconv.ParseInt(epoch, 10, 64)
		if err != nil {
			return "", fmt.Errorf("SOURCE_DATE_EPOCH is not an integer: %w", err)
		}
		return time.Unix(secs, 0).UTC().Format(time.RFC3339), nil
	}
	return time.Now().UTC().Format(time.RFC3339), nil
}
