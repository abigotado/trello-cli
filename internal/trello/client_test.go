package trello

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abigotado-niko/trello-cli/internal/errx"
)

// newTestClient builds a client against srv that records sleeps instead of
// performing them, so retry and pacing behavior is asserted without spending
// wall-clock time.
func newTestClient(t *testing.T, srv *httptest.Server) (*Client, *[]time.Duration) {
	t.Helper()
	var slept []time.Duration
	c := New(srv.URL, Credentials{APIKey: "k", Token: "t"}, 4,
		WithHTTPClient(srv.Client()),
		WithSleep(func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		}),
		// Deterministic backoff, so assertions are on the policy, not on luck.
		WithJitter(func(d time.Duration) time.Duration { return d }),
	)
	return c, &slept
}

func TestGetDecodesSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("key"); got != "k" {
			t.Errorf("key query = %q, want %q", got, "k")
		}
		if got := r.URL.Query().Get("token"); got != "t" {
			t.Errorf("token query = %q, want %q", got, "t")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","username":"nik","fullName":"Nik"}`))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv)
	got, err := c.Me(context.Background())
	if err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if got.Username != "nik" || got.ID != "1" {
		t.Errorf("Me() = %+v", got)
	}
}

// Trello answers 401 with a short text/plain body. A client that assumes a
// JSON envelope fails to parse it and reports a decode error instead of an
// auth error, sending the caller down entirely the wrong path.
func TestUnauthorizedWithPlainTextBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid key"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv)
	_, err := c.Me(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := errx.ExitCode(err); got != errx.CodeAuth {
		t.Errorf("exit code = %d, want %d (auth)", got, errx.CodeAuth)
	}
	if !strings.Contains(err.Error(), "invalid key") {
		t.Errorf("error should carry the server's message, got %q", err.Error())
	}
}

func TestStatusMapping(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		contentType string
		want        errx.Code
	}{
		{"404 is not found", http.StatusNotFound, `{"message":"gone"}`, "application/json", errx.CodeNotFound},
		{"400 is usage", http.StatusBadRequest, `{"message":"invalid idList"}`, "application/json", errx.CodeUsage},
		{"500 is retryable", http.StatusInternalServerError, "boom", "text/plain", errx.CodeRetryable},
		{"418 is internal", http.StatusTeapot, "?", "text/plain", errx.CodeInternal},
		{"empty body does not panic", http.StatusNotFound, "", "text/plain", errx.CodeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c, _ := newTestClient(t, srv)
			_, err := c.Me(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errx.ExitCode(err); got != tt.want {
				t.Errorf("exit code = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRateLimitHonorsRetryAfterThenSucceeds(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"API_TOKEN_LIMIT_EXCEEDED","message":"Rate limit exceeded"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"1","username":"nik"}`))
	}))
	defer srv.Close()

	c, slept := newTestClient(t, srv)
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatalf("Me() error = %v", err)
	}
	if calls != 2 {
		t.Errorf("server saw %d calls, want 2", calls)
	}
	if len(*slept) != 1 || (*slept)[0] != 7*time.Second {
		t.Errorf("slept = %v, want exactly [7s] from Retry-After", *slept)
	}
}

func TestRateLimitExhaustsAttempts(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"API_KEY_LIMIT_EXCEEDED","message":"Rate limit exceeded"}`))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv)
	_, err := c.Me(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := errx.ExitCode(err); got != errx.CodeRetryable {
		t.Errorf("exit code = %d, want %d (retryable)", got, errx.CodeRetryable)
	}
	// Trello returns 429 for the rest of the window once a key exceeds 200
	// rejections in it, so the retry budget must stay small.
	if calls != maxAttempts {
		t.Errorf("server saw %d calls, want %d", calls, maxAttempts)
	}
	var typed *errx.Error
	if errors.As(err, &typed) && typed.Reason != "API_KEY_LIMIT_EXCEEDED" {
		t.Errorf("reason = %q, want the server's error code", typed.Reason)
	}
}

// Pacing is derived from the headers the server returns on every response,
// which is what makes it correct across separate invocations of this
// short-lived binary. A client-side bucket could not be.
func TestPacingFollowsRateLimitHeaders(t *testing.T) {
	tests := []struct {
		name      string
		remaining string
		max       string
		interval  string
		wantSleep bool
	}{
		{"plenty of budget does not pace", "99", "100", "10000", false},
		{"exhausted budget paces", "2", "100", "10000", true},
		{"missing headers do not pace", "", "", "", false},
		{"garbage headers do not pace", "abc", "100", "10000", false},
		{"zero max does not divide by zero", "0", "0", "10000", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.remaining != "" {
					w.Header().Set("x-rate-limit-api-token-remaining", tt.remaining)
				}
				if tt.max != "" {
					w.Header().Set("x-rate-limit-api-token-max", tt.max)
				}
				if tt.interval != "" {
					w.Header().Set("x-rate-limit-api-token-interval-ms", tt.interval)
				}
				_, _ = w.Write([]byte(`{"id":"1"}`))
			}))
			defer srv.Close()

			c, slept := newTestClient(t, srv)
			ctx := context.Background()
			if _, err := c.Me(ctx); err != nil {
				t.Fatalf("first call: %v", err)
			}
			// The delay applies before the *next* request, not the one that
			// reported the headers.
			if _, err := c.Me(ctx); err != nil {
				t.Fatalf("second call: %v", err)
			}
			if got := len(*slept) > 0; got != tt.wantSleep {
				t.Errorf("paced = %v, want %v (slept=%v)", got, tt.wantSleep, *slept)
			}
		})
	}
}

func TestIsSerialRoute(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"search", true},
		{"/search", true},
		{"search/members", true},
		{"membersSearch", true},
		{"members", true},
		{"members/me", true},
		{"members/5f2b", true},
		// Nested resources are explicitly exempt from the /1/members limit.
		// Treating them as serial would throttle the resolver's hottest route
		// to 100 requests per 15 minutes for no reason.
		{"members/me/boards", false},
		{"members/me/cards", false},
		{"boards/abc", false},
		{"boards/abc/lists", false},
		{"cards/abc", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isSerialRoute(tt.path); got != tt.want {
				t.Errorf("isSerialRoute(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// Trello takes the key and token as query parameters, so anything derived from
// a request URL carries live credentials. Every path to a log or a user must
// redact them.
func TestRedactURLRemovesCredentials(t *testing.T) {
	got := RedactURL("https://api.trello.com/1/members/me?fields=id&key=SECRETKEY&token=SECRETTOKEN")
	for _, leaked := range []string{"SECRETKEY", "SECRETTOKEN"} {
		if strings.Contains(got, leaked) {
			t.Errorf("RedactURL leaked %q: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "fields=id") {
		t.Errorf("RedactURL dropped a non-secret parameter: %s", got)
	}
}

func TestNetworkFailureIsRetryableAndRedacted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	c := New(url, Credentials{APIKey: "SECRETKEY", Token: "SECRETTOKEN"}, 1,
		WithSleep(func(context.Context, time.Duration) error { return nil }),
		WithJitter(func(d time.Duration) time.Duration { return d }),
	)
	_, err := c.Me(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := errx.ExitCode(err); got != errx.CodeRetryable {
		t.Errorf("exit code = %d, want %d (retryable)", got, errx.CodeRetryable)
	}
	for _, leaked := range []string{"SECRETKEY", "SECRETTOKEN"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("error leaked %q: %s", leaked, err.Error())
		}
	}
}

func TestContextCancellationIsNotRetried(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := New(srv.URL, Credentials{APIKey: "k", Token: "t"}, 1,
		WithHTTPClient(srv.Client()),
		WithSleep(func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		}),
	)
	_, err := c.Me(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("server saw %d calls, want 1: a cancelled context must stop the retry loop", calls)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"seconds", "5", 5 * time.Second},
		{"padded seconds", " 5 ", 5 * time.Second},
		{"zero", "0", 0},
		{"empty", "", 0},
		{"garbage", "soon", 0},
		{"past http date", "Mon, 02 Jan 2006 15:04:05 GMT", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRetryAfter(tt.value); got != tt.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

// The exit-code contract exists to tell a caller whether to retry. A timeout
// is the most common transient failure this tool has, and returning ctx.Err()
// raw made it exit 1 with "do not retry". This is the test that fails if the
// translation is ever removed.
func TestTimeoutIsRetryableNotInternal(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer func() { close(block); srv.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	c := New(srv.URL, Credentials{APIKey: "k", Token: "t"}, 1, WithHTTPClient(srv.Client()))
	_, err := c.Me(ctx)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if got := errx.ExitCode(err); got != errx.CodeRetryable {
		t.Errorf("exit code = %d, want %d (retryable); a timeout must never be reported as an internal defect", got, errx.CodeRetryable)
	}
	var typed *errx.Error
	if errors.As(err, &typed) && typed.Reason != "TIMEOUT" {
		t.Errorf("reason = %q, want TIMEOUT", typed.Reason)
	}
	// The cause must survive so callers can still match on it.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("the underlying context error was dropped")
	}
}

// testing.md names "429 with and without Retry-After". Without the header the
// only thing standing between a rate-limited key and a retry storm is the
// backoff schedule, which had no coverage.
func TestRateLimitWithoutRetryAfterUsesExponentialBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"API_KEY_LIMIT_EXCEEDED"}`))
	}))
	defer srv.Close()

	c, slept := newTestClient(t, srv)
	if _, err := c.Me(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	// maxAttempts sends, so maxAttempts-1 sleeps, doubling each time.
	want := []time.Duration{500 * time.Millisecond, time.Second}
	if len(*slept) != len(want) {
		t.Fatalf("slept %v, want %v", *slept, want)
	}
	for i, d := range want {
		if (*slept)[i] != d {
			t.Errorf("sleep %d = %v, want %v", i, (*slept)[i], d)
		}
	}
}

func TestRetryAfterAcceptsAFutureHTTPDate(t *testing.T) {
	future := time.Now().UTC().Add(30 * time.Second).Format(http.TimeFormat)
	got := parseRetryAfter(future)
	if got <= 0 || got > 31*time.Second {
		t.Errorf("parseRetryAfter(future date) = %v, want roughly 30s", got)
	}
}

// isSerialRoute is only a predicate; this asserts do() actually honors it.
// Two concurrent searches must not overlap on the server, while two nested
// member routes must, or the resolver's fan-out is pointlessly serialized.
func TestSerialRoutesDoNotOverlap(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		wantOverlap bool
	}{
		{"search serializes", "search", false},
		{"nested member route runs in parallel", "members/me/boards", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var inFlight, peak int
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				inFlight++
				if inFlight > peak {
					peak = inFlight
				}
				mu.Unlock()
				<-release
				mu.Lock()
				inFlight--
				mu.Unlock()
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			c, _ := newTestClient(t, srv)
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					var out map[string]any
					_ = c.Get(context.Background(), tt.path, nil, &out)
				}()
			}
			// Give both goroutines time to reach the server before releasing.
			deadline := time.Now().Add(2 * time.Second)
			for {
				mu.Lock()
				reached := peak
				mu.Unlock()
				if reached == 2 || time.Now().After(deadline) {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			close(release)
			wg.Wait()

			mu.Lock()
			got := peak
			mu.Unlock()
			if tt.wantOverlap && got != 2 {
				t.Errorf("peak in-flight = %d, want 2: nested routes are exempt and must stay parallel", got)
			}
			if !tt.wantOverlap && got != 1 {
				t.Errorf("peak in-flight = %d, want 1: this route must be serialized", got)
			}
		})
	}
}
