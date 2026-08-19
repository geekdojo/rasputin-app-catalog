// Package registry answers one question against a container registry: which
// platforms does this image reference actually publish?
//
// This is a PUBLISH-ONLY check by design (ADR-0006 Decision 8). A control plane
// must never make an outbound call to decide whether to load a tile, so this
// lives here and its answer is baked into the bundle at publish time.
//
// It exists because a tile claiming arm64 that does not run on the Pi is the
// single most expensive way to fail: it passes every static check, ships, and
// breaks on the hardware half the fleet is built from. The evaluation process
// says it in as many words — don't trust the tag, pull it.
package registry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

var accept = strings.Join([]string{
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
}, ", ")

var client = &http.Client{Timeout: 30 * time.Second}

// Platforms returns the os/arch pairs an image reference publishes, e.g.
// "linux/amd64". A single-architecture image returns an empty slice: the
// registry answers with a plain manifest that names no platform, and the caller
// must not read that as "publishes nothing".
func Platforms(ref string) ([]string, error) {
	reg, repo, version := split(ref)
	tok, err := token(reg, repo)
	if err != nil {
		return nil, err
	}
	host := reg
	if reg == "docker.io" {
		host = "registry-1.docker.io"
	}

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://%s/v2/%s/manifests/%s", host, repo, version), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned %s for %s", resp.Status, ref)
	}

	var idx struct {
		Manifests []struct {
			Platform struct {
				OS      string `json:"os"`
				Arch    string `json:"architecture"`
				Variant string `json:"variant"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&idx); err != nil {
		return nil, fmt.Errorf("decode manifest for %s: %w", ref, err)
	}

	seen := map[string]bool{}
	for _, m := range idx.Manifests {
		// Attestation entries carry the literal platform "unknown/unknown" and
		// are not runnable images; counting them would let a tile claim an
		// architecture it only has a signature for.
		if m.Platform.OS == "unknown" || m.Platform.Arch == "unknown" || m.Platform.OS == "" {
			continue
		}
		seen[m.Platform.OS+"/"+m.Platform.Arch] = true
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func token(reg, repo string) (string, error) {
	var u string
	if reg == "docker.io" {
		u = "https://auth.docker.io/token?service=registry.docker.io&scope=repository:" + repo + ":pull"
	} else {
		u = "https://" + reg + "/token?scope=repository:" + repo + ":pull"
	}
	resp, err := client.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var t struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	if t.Token == "" {
		return "", fmt.Errorf("no pull token for %s/%s", reg, repo)
	}
	return t.Token, nil
}

// split breaks "ghcr.io/owner/img:tag@sha256:..." into registry, repository and
// the version to request. A digest is preferred over the tag when both are
// present — that is the whole point of pinning.
func split(ref string) (reg, repo, version string) {
	name := ref
	version = ""
	if i := strings.Index(ref, "@"); i >= 0 {
		name, version = ref[:i], ref[i+1:]
	}
	if i := strings.LastIndex(name, ":"); i >= 0 && !strings.Contains(name[i:], "/") {
		if version == "" {
			version = name[i+1:]
		}
		name = name[:i]
	}
	if version == "" {
		version = "latest"
	}
	// A leading segment containing a dot or a port is a registry host; anything
	// else is Docker Hub, where a single-segment name means an official image.
	if first, rest, ok := strings.Cut(name, "/"); ok && (strings.Contains(first, ".") || strings.Contains(first, ":")) {
		return first, rest, version
	}
	if !strings.Contains(name, "/") {
		return "docker.io", "library/" + name, version
	}
	return "docker.io", name, version
}
