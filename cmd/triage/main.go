// Command triage validates an app request before a human — or an agent — spends
// time on it.
//
// SECURITY POSTURE. Everything this reads is attacker-controlled: the issue body
// is written by whoever opened it, and the URLs in it point wherever they like.
// Three rules follow, and they are why this is a Go program rather than a few
// lines of shell in the workflow:
//
//  1. The issue body arrives via an ENVIRONMENT VARIABLE, never interpolated
//     into a shell command. `run: echo "${{ github.event.issue.body }}"` is a
//     remote code execution hole — a body containing a backtick or $(...) runs
//     on the runner. Passing it through env is what closes it.
//  2. Nothing here fetches a URL the issue supplies. The upstream link is
//     checked for SHAPE and echoed back for a human to click; following it would
//     let an issue steer this job's network access.
//  3. The only network call is to a container registry, for an image reference
//     that has already been parsed and validated.
//
// Its output is a verdict and a comment. It never labels something ready to
// build, because that is a curation judgement and this is a completeness check.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/geekdojo/rasputin-app-catalog/internal/registry"
	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

// GitHub renders an issue form as "### Label" followed by the value. An empty
// optional field renders the literal "_No response_".
var sectionRe = regexp.MustCompile(`(?m)^###\s+(.+?)\s*$`)

func parseForm(body string) map[string]string {
	out := map[string]string{}
	idx := sectionRe.FindAllStringSubmatchIndex(body, -1)
	for i, m := range idx {
		label := strings.TrimSpace(body[m[2]:m[3]])
		end := len(body)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		val := strings.TrimSpace(body[m[1]:end])
		if val == "_No response_" {
			val = ""
		}
		out[strings.ToLower(label)] = val
	}
	return out
}

type problem struct {
	blocking bool
	text     string
}

// echoUser prepares an attacker-supplied value for inclusion in a comment we
// post publicly. Injection into the RUNNER is already closed by passing the body
// through env; this closes the smaller hole at the other end. A value echoed
// verbatim into a public comment can @-mention people — notification spam
// available to anyone who opens an issue — smuggle links, or run long enough to
// bury the verdict it is supposed to explain.
//
// Backticks are STRIPPED rather than escaped because the value is rendered
// inside a code span: leaving one in breaks out of the span and lets the rest be
// interpreted as markdown.
func echoUser(s string) string {
	s = strings.ReplaceAll(s, "`", "")
	s = strings.ReplaceAll(s, "@", "@\u200b") // zero-width space defuses the mention
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

func main() {
	body := os.Getenv("ISSUE_BODY")
	if strings.TrimSpace(body) == "" {
		fmt.Println("no issue body supplied")
		os.Exit(2)
	}
	f := parseForm(body)
	var problems []problem

	id := f["proposed tile id"]
	switch {
	case id == "":
		problems = append(problems, problem{true, "No tile id given."})
	case !tileschema.ValidDNSLabel(id):
		problems = append(problems, problem{true, fmt.Sprintf(
			"`%s` is not usable as a tile id. It becomes part of the app's address on a cluster, so it must be a DNS label: lowercase letters, digits and hyphens, not starting or ending with a hyphen.", echoUser(id))})
	default:
		switch tileStatus(id) {
		case statusAvailable:
			problems = append(problems, problem{true, fmt.Sprintf(
				"`%s` already ships in the catalog. If it is broken, out of date, or has relicensed, that is a different report.", echoUser(id))})
		case statusPreview:
			// NOT a blocking failure. A preview tile is on the roadmap and not
			// installable, so asking for it is a legitimate request to prioritize
			// it — arguably the most useful signal the intake can collect, since
			// it is demand for something already judged worth carrying.
			problems = append(problems, problem{false, fmt.Sprintf(
				"`%s` is already a **preview** tile — authored, on the roadmap, not yet installable. It becomes installable once it clears the hardware bench, so this request is read as a vote to prioritize that rather than as a new app.", echoUser(id))})
		}
	}

	if strings.TrimSpace(f["app name"]) == "" {
		problems = append(problems, problem{true, "No app name given."})
	}
	if u := f["upstream project"]; !strings.HasPrefix(u, "https://") {
		problems = append(problems, problem{true, "Upstream project must be an `https://` link to the source repository."})
	}
	if strings.TrimSpace(f["would you screenshot this and show someone?"]) == "" {
		problems = append(problems, problem{true, "The screenshot question is the one the catalog is curated on — it needs an answer."})
	}

	archFound := ""
	img := strings.TrimSpace(f["container image"])
	switch {
	case img == "":
		problems = append(problems, problem{true, "No container image given."})
	case strings.HasSuffix(img, ":latest") || !strings.Contains(lastSegment(img), ":"):
		problems = append(problems, problem{true,
			"The image needs a specific version tag. `latest` moves under us, so it cannot be carried."})
	default:
		declared := parseArch(f["which architectures does it support?"])
		if plats, err := registry.Platforms(img); err != nil {
			problems = append(problems, problem{true, fmt.Sprintf(
				"Could not read `%s` from its registry (%v). Check the name, and that the image is public.", echoUser(img), err)})
		} else {
			archFound = strings.Join(plats, ", ")
			problems = append(problems, archVerdict(img, declared, plats)...)
		}
	}

	blocking := 0
	for _, p := range problems {
		if p.blocking {
			blocking++
		}
	}

	fmt.Println(render(id, img, archFound, problems, blocking))
	if blocking > 0 {
		writeOutput("verdict", "incomplete")
		os.Exit(1)
	}
	writeOutput("verdict", "ready-for-review")
	return
}

// archVerdict verifies the tile's CLAIM against the registry. It does not demand
// both architectures.
//
// That distinction was got wrong first time round. ACC-1's bench criterion is
// "prove the published multi-arch image actually runs on the Pi, don't trust the
// tag" — a VERIFICATION step, which this turned into a GATE requiring both. The
// product had already decided otherwise: `arch` is a first-class tile field
// accepting arm64 or amd64 alone, install is arch-gated, and the install drawer
// filters the node picker by architecture. Single-arch apps are supported by
// design; the rubric treats limited cluster fit as a scoring demerit, not a
// disqualification.
//
// What IS worth failing on is a tile claiming an architecture it does not
// publish, because that surfaces as a failed install on somebody's Pi rather
// than as a rejected request here.
//
// archVerdict is the decision, split from the fetch so every branch is testable
// without a registry. Hunting for a genuinely single-arch image to exercise the
// mismatch path is how that case ends up untested.
func archVerdict(img, declared string, plats []string) []problem {
	if len(plats) == 0 {
		// A plain (non-index) manifest names no platform. We cannot tell what it
		// is from here, so say so rather than guessing.
		return []problem{{false, fmt.Sprintf(
			"`%s` publishes a single-architecture manifest that does not declare its platform, so the architecture could not be confirmed automatically. A human will check it.", echoUser(img))}}
	}

	have := map[string]bool{}
	for _, p := range plats {
		have[p] = true
	}

	// "not sure" is answered rather than punished — the registry already knows.
	if declared == archUnknown {
		return []problem{{false, fmt.Sprintf(
			"Architecture checked for you: `%s` publishes **%s**. The tile will be recorded as `%s`.",
			echoUser(img), strings.Join(plats, ", "), inferArch(have))}}
	}

	var missing []string
	for _, need := range requiredPlatforms(declared) {
		if !have[need] {
			missing = append(missing, need)
		}
	}
	if len(missing) > 0 {
		return []problem{{true, fmt.Sprintf(
			"The request says **%s**, but `%s` publishes only %s — missing %s. Either the claim or the image is wrong.",
			declared, echoUser(img), strings.Join(plats, ", "), strings.Join(missing, " and "))}}
	}
	return nil
}

const (
	archBoth    = "both"
	archArm64   = "arm64"
	archAmd64   = "amd64"
	archUnknown = "unknown"
)

// parseArch maps the dropdown's prose to a tile `arch` value. The option labels
// carry explanatory text, so match on a distinguishing prefix rather than the
// whole string — reworded labels should not silently become "unknown".
func parseArch(s string) string {
	switch l := strings.ToLower(strings.TrimSpace(s)); {
	case strings.HasPrefix(l, "both"):
		return archBoth
	case strings.HasPrefix(l, "arm64"):
		return archArm64
	case strings.HasPrefix(l, "amd64"):
		return archAmd64
	default:
		return archUnknown
	}
}

func requiredPlatforms(declared string) []string {
	switch declared {
	case archArm64:
		return []string{"linux/arm64"}
	case archAmd64:
		return []string{"linux/amd64"}
	default:
		return []string{"linux/amd64", "linux/arm64"}
	}
}

func inferArch(have map[string]bool) string {
	switch {
	case have["linux/amd64"] && have["linux/arm64"]:
		return archBoth
	case have["linux/arm64"]:
		return archArm64
	case have["linux/amd64"]:
		return archAmd64
	}
	return archUnknown
}

func render(id, img, archNote string, problems []problem, blocking int) string {
	var b strings.Builder
	if blocking == 0 {
		b.WriteString("### Automated check: passed\n\n")
		if archNote != "" {
			fmt.Fprintf(&b, "The request is complete, and the image publishes %s.\n\n", archNote)
		} else {
			b.WriteString("The request is complete.\n\n")
		}
		for _, p := range problems {
			if !p.blocking {
				fmt.Fprintf(&b, "- %s\n", p.text)
			}
		}
		if len(problems) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("**This is not an acceptance.** It means the request is worth a human reading. ")
		b.WriteString("The catalog is curated rather than comprehensive, so a complete, technically sound ")
		b.WriteString("request can still be declined because it does not fit the set.\n")
		return b.String()
	}
	b.WriteString("### Automated check: needs changes\n\n")
	for _, p := range problems {
		if p.blocking {
			fmt.Fprintf(&b, "- %s\n", p.text)
		}
	}
	b.WriteString("\nEdit the issue and these checks run again — no need to open a new one.\n")
	return b.String()
}

const (
	statusNone      = ""
	statusAvailable = "available"
	statusPreview   = "preview"
)

// tileStatus reports whether we already carry this id, and in what sense. The
// distinction matters to the requester: "we ship this" and "we plan to ship
// this" deserve different answers, and conflating them turns a useful
// prioritization signal into a rejection.
func tileStatus(id string) string {
	blob, err := os.ReadFile("tiles/" + id + "/tile.json")
	if err != nil {
		return statusNone
	}
	var t tileschema.Tile
	if json.Unmarshal(blob, &t) != nil {
		return statusAvailable // present but unreadable: treat as carried
	}
	if t.Status == tileschema.StatusPreview {
		return statusPreview
	}
	return statusAvailable
}

func lastSegment(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

func writeOutput(k, v string) {
	if p := os.Getenv("GITHUB_OUTPUT"); p != "" {
		if fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(fh, "%s=%s\n", k, v)
			fh.Close()
		}
	}
}
