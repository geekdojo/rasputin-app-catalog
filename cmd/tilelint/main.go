// Command tilelint validates the tile corpus before it is published.
//
// It is a CALLER of the shared tileschema validators, never a second
// implementation of them (ADR-0006 Decision 8): a duplicate ruleset drifts, and
// the drift is silent because each side stays internally green. What lives here
// is only what genuinely cannot run on the control plane — deriving safety
// facts from compose, and asking a registry which platforms an image publishes.
//
//	tilelint            validate every tile (offline checks only)
//	tilelint -arch      also probe registries for architecture claims
//	tilelint -tile jellyfin
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/geekdojo/rasputin-app-catalog/internal/compose"
	"github.com/geekdojo/rasputin-app-catalog/internal/corpus"
	"github.com/geekdojo/rasputin-app-catalog/internal/registry"
	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

func main() {
	var (
		root         = flag.String("root", "tiles", "tile corpus directory")
		probe        = flag.Bool("arch", false, "probe registries to verify architecture claims (network)")
		only         = flag.String("tile", "", "validate a single tile by id")
		failures     int
		checked      int
		privileged   int
		unverifiable int
	)
	flag.Parse()

	ids, err := corpus.IDs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tilelint:", err)
		os.Exit(2)
	}

	for _, id := range ids {
		if *only != "" && id != *only {
			continue
		}
		checked++
		problems, notices, unverified := checkTile(*root, id, *probe)
		if unverified {
			unverifiable++
		}
		for _, problem := range problems {
			fmt.Printf("  x %-22s %s\n", id, problem)
			failures++
		}
		// Notices never fail the run. They exist because a privilege a tile
		// takes is worth a human seeing even when policy permits it — and
		// because the submission pipeline opens PRs a reviewer skims (#195).
		for _, n := range notices {
			fmt.Printf("  ! %-22s %s\n", id, n)
			privileged++
		}
	}

	if *only != "" && checked == 0 {
		fmt.Fprintf(os.Stderr, "tilelint: no tile with id %q\n", *only)
		os.Exit(2)
	}
	fmt.Printf("\n%d tile(s) checked, %d problem(s), %d privilege notice(s)\n", checked, failures, privileged)
	if unverifiable > 0 {
		// Said once, plainly, rather than defaulted away. These tiles have no
		// stack, so their privilege is not routine — it is uncomputed, and it
		// stays that way until the day each one ships a compose.
		fmt.Printf("%d preview tile(s) ship no compose: their privilege is UNVERIFIED, not routine\n", unverifiable)
	}
	if failures > 0 {
		os.Exit(1)
	}
}

// checkTile returns every problem with one tile rather than the first, so a
// contributor sees the whole list in one run instead of peeling them off across
// six pushes. Notices are the second return: things a reviewer should SEE that
// are not, today, things the validator refuses.
func checkTile(root, id string, probe bool) (problems, notices []string, unverified bool) {
	// One loader, shared with the bundle builder (internal/corpus). Two would
	// drift, and the drift would be silent — the linter passing a corpus the
	// builder then published differently.
	l, err := corpus.Load(root, id)
	if err != nil {
		return []string{err.Error()}, nil, false
	}
	tile := l.Tile

	if err := tileschema.ValidateTile(tile); err != nil {
		problems = append(problems, err.Error())
	}

	// Safety runs whenever a stack EXISTS, not only when the tile is available.
	// A preview tile with a compose file would otherwise carry an unchecked
	// stack behind the preview flag until the day someone flips it.
	if !l.HasCompose {
		return problems, unverifiablePrivilege(tile), true
	}
	facts, err := compose.Extract([]byte(l.Compose))
	if err != nil {
		return append(problems, err.Error()), nil, false
	}
	if err := tileschema.ValidateTileSafety(tile, facts); err != nil {
		problems = append(problems, err.Error())
		// The actionable half. Grant strings are a derived vocabulary, so
		// asking a contributor to hand-write them is asking them to guess at
		// an internal format — and a guess that fails the lint five times is
		// how a submission pipeline loses people. Print what to paste; the
		// snippet already carries the why placeholder, so the missing-why
		// check below stays quiet rather than piling a second complaint onto
		// a tile that has not declared anything yet.
		if snippetFixes(tile, facts) {
			problems = append(problems, "declare it in tile.json:\n"+declarationSnippet(tile, facts))
		}
	} else if d := tileschema.DerivePrivilege(facts); d.Tier != tileschema.TierRoutine && strings.TrimSpace(tile.DeclaredPrivilege().Why) == "" {
		// ADR-0006 Decision 12c: consent needs an explainer, and a machine
		// cannot tell a good reason from a bad one — but it can tell a missing
		// one. Enforced HERE rather than in tileschema because it is editorial
		// policy about what the catalog vouches for, not a rule a cluster must
		// apply to a bundle it fetched.
		problems = append(problems, fmt.Sprintf("takes %q privilege with no privilege.why — the consent prompt would ask an owner to approve %s with no reason given",
			d.Tier, strings.Join(d.Grants, ", ")))
	}

	if probe {
		problems = append(problems, archProblems(tile, facts)...)
	}
	return problems, privilegeNotices(tile, facts), false
}

// unverifiablePrivilege is what a PREVIEW tile with no compose gets.
//
// Silence here would read as "routine", and that is the one reading it must
// not have: nothing has been computed at all. A preview tile is metadata only,
// so its declaration is unchecked until the day it ships a stack, which is
// also the day it becomes installable.
//
// Most of the corpus is preview, so the UNDECLARED case is reported once in
// the run summary rather than once per tile — eighteen identical lines is how
// a reviewer learns to skip this section. A preview tile that DOES declare
// something gets its own line, because a declaration nothing can check is the
// case actually worth a second look.
func unverifiablePrivilege(t tileschema.Tile) []string {
	declared := t.DeclaredPrivilege()
	if declared.Tier == "" {
		return nil
	}
	return []string{fmt.Sprintf("declares privilege %q but there is no compose to check it against — unverified until this tile ships a stack", declared.Tier)}
}

// snippetFixes reports whether pasting the derived declaration would actually
// make this tile valid.
//
// It answers that by running the SHARED validator against its own suggestion
// rather than reasoning about which failures a declaration can fix. Two
// failures it cannot: a trust-chain mount (Decision 12e is absolute, so no
// tier permits it) and a tag-pinned image. Printing "declare it in tile.json"
// for either sends a contributor to paste a block, re-run, and hit the same
// error — worse than printing nothing, because it looks like the answer.
func snippetFixes(t tileschema.Tile, f tileschema.SafetyFacts) bool {
	fixed := t
	derived := tileschema.DerivePrivilege(f)
	derived.Why = t.DeclaredPrivilege().Why
	fixed.Privilege = &derived
	fixed.Requires = withCapability(t.Requires)
	return tileschema.ValidateTileSafety(fixed, f) == nil
}

// declarationSnippet renders the derived privilege as the tile.json fragment
// that would satisfy it. Built by marshalling the real struct, so the field
// names cannot drift from the ones the reader parses.
func declarationSnippet(t tileschema.Tile, f tileschema.SafetyFacts) string {
	d := tileschema.DerivePrivilege(f)
	if why := strings.TrimSpace(t.DeclaredPrivilege().Why); why != "" {
		d.Why = why
	} else {
		d.Why = "TODO: one line an owner reads before consenting"
	}

	var b strings.Builder
	if d.Tier != tileschema.TierRoutine {
		req, _ := json.Marshal(withCapability(t.Requires))
		fmt.Fprintf(&b, "      \"requires\": %s,\n", req)
	}
	body, err := json.MarshalIndent(d, "      ", "  ")
	if err != nil {
		return "      (could not render: " + err.Error() + ")"
	}
	fmt.Fprintf(&b, "      \"privilege\": %s", body)
	return b.String()
}

// withCapability adds the must-understand capability without duplicating it.
func withCapability(requires []string) []string {
	for _, r := range requires {
		if r == tileschema.CapabilityPrivilegeTiers {
			return requires
		}
	}
	return append(append([]string{}, requires...), tileschema.CapabilityPrivilegeTiers)
}

// privilegeNotices renders the privilege a stack takes, so it is visible on a
// pull request that a human skims.
//
// #195 captured the facts and ruled on none of them; Decision 12 (#198) turned
// them into a tier and a grant list that the tile must DECLARE. So this no
// longer enumerates raw compose keys — it prints the same derivation the
// control plane will run, which is the thing a reviewer actually needs to
// agree with. Decision 8: one derivation, and this is a caller of it.
//
// Notices never fail the run. Everything here is either already permitted or
// already reported as a problem; what is left is the reviewer's judgement.
func privilegeNotices(t tileschema.Tile, f tileschema.SafetyFacts) []string {
	derived := tileschema.DerivePrivilege(f)
	var out []string

	if derived.Tier != tileschema.TierRoutine || len(derived.Grants) > 0 {
		line := "privilege " + derived.Tier
		if derived.DockerSocket {
			line += " + CONTAINER RUNTIME SOCKET"
		}
		if len(derived.Grants) > 0 {
			line += ": " + strings.Join(derived.Grants, " ")
		}
		out = append(out, line)
	}

	// Over-declaration is permitted by the validator on purpose — a tile may
	// be conservative. It is still worth saying, because a badge scarier than
	// the stack teaches an owner to click through the next one.
	if extra := overDeclared(t.DeclaredPrivilege(), derived); len(extra) > 0 {
		out = append(out, "declares privilege it does not take: "+strings.Join(extra, " "))
	}
	if tileschema.TierRank[t.DeclaredPrivilege().EffectiveTier()] > tileschema.TierRank[derived.Tier] {
		out = append(out, fmt.Sprintf("declares tier %q for a %q stack", t.DeclaredPrivilege().EffectiveTier(), derived.Tier))
	}
	return out
}

// overDeclared returns declared grants the stack does not actually take.
func overDeclared(declared, derived tileschema.Privilege) []string {
	takes := map[string]bool{}
	for _, g := range derived.Grants {
		takes[g] = true
	}
	var extra []string
	for _, g := range declared.Grants {
		if !takes[strings.TrimSpace(g)] {
			extra = append(extra, g)
		}
	}
	sort.Strings(extra)
	return extra
}

// archProblems verifies the tile's arch claim against what the registry
// actually publishes. "both" is the claim worth checking hardest: it is the
// default a contributor reaches for, and getting it wrong means the tile
// installs happily and dies on every Pi in the fleet.
func archProblems(tile tileschema.Tile, facts tileschema.SafetyFacts) []string {
	want := map[string][]string{
		"both":  {"linux/amd64", "linux/arm64"},
		"arm64": {"linux/arm64"},
		"amd64": {"linux/amd64"},
	}[tile.Arch]

	var problems []string
	for _, img := range facts.Images {
		got, err := registry.Platforms(img)
		if err != nil {
			problems = append(problems, fmt.Sprintf("arch probe %s: %v", img, err))
			continue
		}
		if len(got) == 0 {
			problems = append(problems, fmt.Sprintf("%s publishes a single-architecture manifest; cannot satisfy arch %q", img, tile.Arch))
			continue
		}
		have := map[string]bool{}
		for _, p := range got {
			have[p] = true
		}
		for _, w := range want {
			if !have[w] {
				problems = append(problems, fmt.Sprintf("tile claims arch %q but %s publishes only %s", tile.Arch, img, strings.Join(got, ", ")))
				break
			}
		}
	}
	return problems
}
