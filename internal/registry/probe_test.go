package registry

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// noSleep swaps the backoff for a recorder, so a test can assert on the delays
// the code CHOSE without paying them. Restored via t.Cleanup.
func noSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	prev := sleep
	sleep = func(d time.Duration) { got = append(got, d) }
	t.Cleanup(func() { sleep = prev })
	return &got
}

func mustGet(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return req
}

func TestRetryable(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusBadGateway, true},
		{http.StatusOK, false},
		// A 404 or a 401 is the registry ANSWERING about this reference. Retrying
		// it would turn a clear "no such image" into a slow, vaguer version of
		// the same failure.
		{http.StatusNotFound, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
	} {
		if got := retryable(tc.status); got != tc.want {
			t.Errorf("retryable(%d) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestWaitBacksOffExponentially(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	for n, w := range want {
		if got := wait(n, http.Header{}); got != w {
			t.Errorf("wait(%d, nil) = %v, want %v", n, got, w)
		}
	}
}

func TestWaitCapsTheBackoff(t *testing.T) {
	if got := wait(20, http.Header{}); got != maxDelay {
		t.Errorf("wait(20) = %v, want the %v cap", got, maxDelay)
	}
}

func TestWaitObeysRetryAfterSeconds(t *testing.T) {
	h := http.Header{"Retry-After": []string{"5"}}
	// The registry's number wins over our guess of 1s, in both directions.
	if got := wait(0, h); got != 5*time.Second {
		t.Errorf("wait with Retry-After: 5 = %v, want 5s", got)
	}
	h.Set("Retry-After", "0")
	if got := wait(2, h); got != 0 {
		t.Errorf("wait with Retry-After: 0 = %v, want 0", got)
	}
}

func TestWaitCapsRetryAfter(t *testing.T) {
	// A registry asking for an hour does not get an hour. We stop waiting and
	// let the error surface, which CI can read, rather than hang the job.
	h := http.Header{"Retry-After": []string{"3600"}}
	if got := wait(0, h); got != maxDelay {
		t.Errorf("wait with Retry-After: 3600 = %v, want the %v cap", got, maxDelay)
	}
}

func TestWaitAcceptsRetryAfterDate(t *testing.T) {
	future := time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
	h := http.Header{"Retry-After": []string{future}}
	got := wait(0, h)
	if got <= 0 || got > 4*time.Second {
		t.Errorf("wait with a date 3s out = %v, want roughly 3s", got)
	}

	// A date already gone means go now, never a negative sleep.
	h.Set("Retry-After", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
	if got := wait(0, h); got != 0 {
		t.Errorf("wait with a past date = %v, want 0", got)
	}
}

func TestWaitIgnoresGarbageRetryAfter(t *testing.T) {
	h := http.Header{"Retry-After": []string{"soon-ish"}}
	if got := wait(1, h); got != 2*time.Second {
		t.Errorf("wait with an unparseable Retry-After = %v, want the 2s backoff", got)
	}
}

// The regression this change exists for: ghcr answers 429, then answers.
func TestGetRetriesPast429(t *testing.T) {
	delays := noSleep(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := get(mustGet(t, srv.URL))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if n := hits.Load(); n != 3 {
		t.Errorf("made %d requests, want 3", n)
	}
	if want := []time.Duration{2 * time.Second, 2 * time.Second}; len(*delays) != len(want) {
		t.Errorf("slept %v, want %v — Retry-After should drive the delay", *delays, want)
	}
}

// A registry that never relents still fails, and fails with ITS status, so the
// log says "429" and not something we invented.
func TestGetGivesUpAndReportsTheRealStatus(t *testing.T) {
	noSleep(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	resp, err := get(mustGet(t, srv.URL))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want the registry's 429", resp.StatusCode)
	}
	if n := hits.Load(); n != attempts {
		t.Errorf("made %d requests, want %d", n, attempts)
	}
}

// A real answer is returned on the first ask. Retrying a 404 would only make a
// clear failure slow.
func TestGetDoesNotRetryARealAnswer(t *testing.T) {
	delays := noSleep(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	resp, err := get(mustGet(t, srv.URL))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("made %d requests, want 1", n)
	}
	if len(*delays) != 0 {
		t.Errorf("slept %v, want no delay", *delays)
	}
}

// A dropped connection is a non-answer too, not a verdict on the tile.
func TestGetRetriesATransportError(t *testing.T) {
	noSleep(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead := srv.URL
	srv.Close() // nothing is listening now

	if _, err := get(mustGet(t, dead)); err == nil {
		t.Fatal("get against a dead server returned no error")
	}
}

func TestGetSurvivesA500Then200(t *testing.T) {
	noSleep(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := get(mustGet(t, srv.URL))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
