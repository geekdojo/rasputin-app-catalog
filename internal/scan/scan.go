// Package scan derives the supply-chain facts behind carrying a tile: which
// licences its image actually contains, and which of its vulnerabilities we
// could do something about.
//
// Carrying a tile in the first-party catalog is an endorsement. This is the
// evidence behind it, and the whole reason it is machine-checked rather than
// left to the maintenance policy's "subscribe to upstream releases", which
// named an obligation with no mechanism behind it.
package scan

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// SourceAvailable licences are the ones that change what Rasputin may ship.
// They are NOT open source, and a tile acquiring one is a rug-pull in progress
// — Planka removing SSO from its community edition is the shape, and the app
// catalog design already flags relicensing as needing a decision rather than a
// note. Finding one FAILS the gate.
var SourceAvailable = []string{"SSPL", "BUSL", "BUSINESS SOURCE", "ELASTIC-2", "ELASTIC LICENSE", "COMMONS-CLAUSE", "COMMONS CLAUSE"}

// Copyleft licences are legitimate and common in this space — Immich is AGPL,
// and refusing them would gut the catalog. They are tracked, not refused: a
// tile that ACQUIRES one on a re-pin is a decision event, which the checked-in
// provenance diff surfaces.
var Copyleft = []string{"AGPL", "GPL-3", "SSPL"}

// Provenance is the checked-in, reviewable summary for one tile. It records
// only what is stable across a base-image bump: 182 distinct licences show up
// in a single Debian-based image, so an inventory diff would fire constantly
// and teach everyone to ignore it. Watchlist hits are the low-noise signal.
type Provenance struct {
	Tile            string   `json:"tile"`
	Image           string   `json:"image"`
	SourceAvailable []string `json:"sourceAvailable"`
	Copyleft        []string `json:"copyleft"`
}

type trivyReport struct {
	Results []struct {
		Class           string                  `json:"Class"`
		Licenses        []struct{ Name string } `json:"Licenses"`
		Vulnerabilities []struct {
			VulnerabilityID  string `json:"VulnerabilityID"`
			PkgName          string `json:"PkgName"`
			InstalledVersion string `json:"InstalledVersion"`
			FixedVersion     string `json:"FixedVersion"`
			Severity         string `json:"Severity"`
		} `json:"Vulnerabilities"`
	} `json:"Results"`
}

// Vuln is one finding worth a human's attention.
type Vuln struct {
	ID, Pkg, Installed, Fixed, Severity string
	// Class is trivy's "os-pkgs" or "lang-pkgs", and the distinction is the
	// whole reason this field exists. A single total conflates two problems
	// with different owners: lang-pkgs are the maintainers' OWN dependency
	// choices, while os-pkgs come from whatever base image they built on and
	// are fixed by an upstream rebuild nobody here can trigger.
	//
	// Measured on uptime-kuma 2026-08-19: v2 looked 8x worse than v1 on the
	// raw total (1475 vs 176) and was actually BETTER on its own dependencies
	// (92 vs 174) — the gap was 1383 stale Debian packages. Acting on the total
	// nearly pinned the catalog to a branch that had shipped once in 20 months.
	Class string
}

// AppLevel reports whether this is the maintainers' own dependency rather than
// something inherited from the base image.
func (v Vuln) AppLevel() bool { return v.Class == "lang-pkgs" }

// Fixable reports whether upstream has shipped a fix. This is the distinction
// the gate turns on: a fixable HIGH means OUR pin is stale and we can act, while
// an unfixed one means the world has a problem and dropping the tile is the only
// lever. Gating on the second would be a permanently red build nobody can green.
func (v Vuln) Fixable() bool { return strings.TrimSpace(v.Fixed) != "" }

// Scan runs trivy against a pinned image reference.
func Scan(tile, image string) (Provenance, []Vuln, error) {
	out, err := exec.Command("trivy", "image", "--quiet", "--format", "json",
		"--scanners", "vuln,license", "--license-full", image).Output()
	if err != nil {
		return Provenance{}, nil, fmt.Errorf("trivy %s: %w", image, err)
	}
	var rep trivyReport
	if err := json.Unmarshal(out, &rep); err != nil {
		return Provenance{}, nil, fmt.Errorf("parse trivy output for %s: %w", image, err)
	}

	p := Provenance{Tile: tile, Image: image, SourceAvailable: []string{}, Copyleft: []string{}}
	sa, cl := map[string]bool{}, map[string]bool{}
	var vulns []Vuln

	for _, r := range rep.Results {
		for _, l := range r.Licenses {
			up := strings.ToUpper(l.Name)
			for _, k := range SourceAvailable {
				if strings.Contains(up, k) {
					sa[l.Name] = true
				}
			}
			for _, k := range Copyleft {
				if strings.Contains(up, k) {
					cl[l.Name] = true
				}
			}
		}
		for _, v := range r.Vulnerabilities {
			if v.Severity == "HIGH" || v.Severity == "CRITICAL" {
				vulns = append(vulns, Vuln{v.VulnerabilityID, v.PkgName, v.InstalledVersion, v.FixedVersion, v.Severity, r.Class})
			}
		}
	}
	p.SourceAvailable, p.Copyleft = keys(sa), keys(cl)
	sort.Slice(vulns, func(i, j int) bool { return vulns[i].ID < vulns[j].ID })
	return p, vulns, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
