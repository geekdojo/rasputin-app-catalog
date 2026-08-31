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
	"strconv"
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

// The probe runs unauthenticated, so on a shared CI egress IP it draws from an
// anonymous quota it shares with every other runner on that address. ghcr
// answered four of the sixteen tiles with 429 on the 2026-08-31 scheduled run;
// the same corpus probed clean from a laptop minutes later. A rate limit is the
// registry asking us to wait, not an answer about the tile, and a lint that
// turns "ask again shortly" into "this tile is broken" reports a defect that
// does not exist. So a retryable answer is retried, and only a real answer —
// or a registry that will not stop saying wait — reaches the caller.
const (
	attempts  = 4
	baseDelay = time.Second
	maxDelay  = 30 * time.Second
)

// sleep is a variable so the tests can exercise the backoff without spending
// the wall-clock time it describes.
var sleep = time.Sleep

// retryable reports whether a status is the registry declining to answer YET.
// 429 is the rate limit this exists for; 5xx is the same shape of answer from
// the other end. Every other status — including 404 and 401 — is a real answer
// about the reference and must be reported as-is, not retried into a timeout.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// wait returns how long to hold off before attempt n (0-based). A registry that
// sends Retry-After has told us the answer and is obeyed over our own guess,
// capped so one hostile or confused header cannot park CI for an hour — past
// the cap we stop waiting and let the error surface, which is a failure the
// logs explain rather than a job that looks hung.
func wait(n int, h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
			return min(time.Duration(secs)*time.Second, maxDelay)
		}
		if t, err := http.ParseTime(v); err == nil {
			// A date already in the past means "go now", not "go backwards".
			if d := time.Until(t); d > 0 {
				return min(d, maxDelay)
			}
			return 0
		}
	}
	return min(baseDelay<<n, maxDelay)
}

// get performs a GET, retrying while the registry says "not yet". The caller
// owns the returned body. Transport errors are retried on the same terms: a
// reset connection is the same class of non-answer as a 429, and treating it as
// a verdict on the tile would be the same mistake.
func get(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error
	for n := range attempts {
		if n > 0 {
			var h http.Header
			if resp != nil {
				h = resp.Header
			}
			sleep(wait(n-1, h))
		}
		if resp != nil {
			resp.Body.Close()
		}
		resp, err = client.Do(req)
		if err != nil {
			continue
		}
		if !retryable(resp.StatusCode) {
			return resp, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return resp, nil
}

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

	resp, err := get(req)
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
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	// The token endpoint shares the anonymous quota with the manifest fetch and
	// declines the same way, so it gets the same treatment. A 429 here surfaced
	// as "no pull token", which named the wrong cause.
	resp, err := get(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry returned %s for a pull token for %s/%s", resp.Status, reg, repo)
	}
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
