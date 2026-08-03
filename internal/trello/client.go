// Package trello is the Trello REST client.
//
// It does not import internal/auth: credentials arrive as a value in [New].
// Importing auth would invert the dependency arrow and make every client test
// depend on an OS keychain.
//
// It also formats no user-facing text. Errors are translated into typed
// internal/errx values; rendering is internal/output's job.
package trello

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abigotado-niko/trello-cli/internal/errx"
)

// maxAttempts bounds how many times one request is sent.
//
// Kept deliberately small: Trello returns 429 for the remainder of a window
// once a key exceeds 200 rejected requests in it, so an aggressive retry loop
// makes the situation strictly worse for every other caller sharing the key.
const maxAttempts = 3

// Credentials is the API key and token pair. It mirrors the shape of
// auth.Credentials without creating a dependency on that package.
type Credentials struct {
	APIKey string
	Token  string
}

// Client talks to the Trello REST API.
type Client struct {
	baseURL string
	creds   Credentials
	http    *http.Client
	log     *slog.Logger

	// sem bounds concurrent requests for commands that fan out.
	sem chan struct{}
	// serial guards the routes Trello gives their own low limits.
	serial sync.Mutex

	// pace is the delay to observe before the next request, derived from the
	// last response's rate-limit headers.
	paceMu sync.Mutex
	pace   time.Duration

	// Injected for tests.
	sleep  func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

// Option customizes a Client.
type Option func(*Client)

// WithHTTPClient replaces the underlying HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithLogger sets the destination for verbose request logging. The logger must
// write to stderr: stdout carries only the response envelope.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// WithSleep replaces the delay function, so tests do not spend real time.
func WithSleep(f func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.sleep = f }
}

// WithJitter replaces backoff jitter, so tests are deterministic.
func WithJitter(f func(time.Duration) time.Duration) Option {
	return func(c *Client) { c.jitter = f }
}

// New builds a Client. concurrency must be at least 1.
func New(baseURL string, creds Credentials, concurrency int, opts ...Option) *Client {
	if concurrency < 1 {
		concurrency = 1
	}
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		creds:   creds,
		http:    &http.Client{},
		log:     slog.New(discardHandler{}),
		sem:     make(chan struct{}, concurrency),
		sleep:   sleepCtx,
		jitter:  defaultJitter,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get performs a GET and decodes the JSON response into out.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return errx.Internal("encode request body: %v", err)
		}
	}

	// Routes with their own low server-side limits are never run in parallel.
	// Nested resources are explicitly exempt from the /1/members limit, so
	// only the bare route is serialized here.
	if isSerialRoute(path) {
		c.serial.Lock()
		defer c.serial.Unlock()
	} else {
		select {
		case c.sem <- struct{}{}:
			defer func() { <-c.sem }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := c.awaitPace(ctx); err != nil {
			return err
		}
		resp, err := c.send(ctx, method, path, query, payload)
		if err != nil {
			// A cancelled or timed-out context is the caller's decision, not a
			// transport failure to retry against.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = errx.Retryable("NETWORK", 0, "%s %s: %v", method, path, redact(err)).Wrap(err)
			if attempt == maxAttempts {
				return lastErr
			}
			if err := c.sleep(ctx, c.backoff(attempt)); err != nil {
				return err
			}
			continue
		}

		retryAfter, retryable, apiErr := c.handle(resp, method, path, out)
		if apiErr == nil {
			return nil
		}
		if !retryable || attempt == maxAttempts {
			return apiErr
		}
		lastErr = apiErr
		delay := retryAfter
		if delay <= 0 {
			delay = c.backoff(attempt)
		}
		c.log.Debug("retrying", "method", method, "path", path, "attempt", attempt, "delay", delay)
		if err := c.sleep(ctx, delay); err != nil {
			return err
		}
	}
	return lastErr
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, payload []byte) (*http.Response, error) {
	if query == nil {
		query = url.Values{}
	} else {
		query = cloneValues(query)
	}
	query.Set("key", c.creds.APIKey)
	query.Set("token", c.creds.Token)

	full := c.baseURL + "/" + strings.TrimLeft(path, "/") + "?" + query.Encode()
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, full, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.log.Debug("request", "method", method, "path", path)
	return c.http.Do(req)
}

// handle consumes the response. It returns the advertised retry delay, whether
// the failure is worth retrying, and the translated error (nil on success).
func (c *Client) handle(resp *http.Response, method, path string, out any) (time.Duration, bool, error) {
	defer func() {
		// Drain before closing so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	c.observePacing(resp.Header)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil {
			return 0, false, nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return 0, false, errx.Internal("decode %s %s response: %v", method, path, err)
		}
		return 0, false, nil
	}

	// Trello does not always answer with JSON. A 401 comes back as a short
	// text/plain body, so this must never assume a decodable envelope.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	apiCode, message := parseAPIError(raw)

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		reason := "UNAUTHORIZED"
		if apiCode != "" {
			reason = apiCode
		}
		return 0, false, errx.Auth(reason, "Trello rejected the credentials: %s", message)

	case resp.StatusCode == http.StatusTooManyRequests:
		delay := parseRetryAfter(resp.Header.Get("Retry-After"))
		reason := "RATE_LIMITED"
		if apiCode != "" {
			reason = apiCode
		}
		return delay, true, errx.Retryable(reason, delay, "Trello rate limit reached: %s", message)

	case resp.StatusCode == http.StatusNotFound:
		return 0, false, &errx.Error{
			Code:    errx.CodeNotFound,
			Reason:  "NOT_FOUND",
			Message: fmt.Sprintf("%s %s: %s", method, path, message),
			Hint:    "verify the object id still exists",
		}

	case resp.StatusCode >= 500:
		return 0, true, errx.Retryable("SERVER_ERROR", 0, "Trello returned %d: %s", resp.StatusCode, message)

	case resp.StatusCode == http.StatusBadRequest:
		return 0, false, errx.Usage("Trello rejected the request: %s", message)

	default:
		return 0, false, errx.Internal("unexpected Trello status %d: %s", resp.StatusCode, message)
	}
}

// observePacing derives a pre-request delay from the rate-limit headers.
//
// This replaces client-side token buckets, which cannot work here: the binary
// is short-lived and re-invoked, so every process would start with a full
// bucket, and the bucket would only ever be a stale replica of a counter the
// server already reports on every response.
func (c *Client) observePacing(h http.Header) {
	remaining, okR := headerInt(h, "x-rate-limit-api-token-remaining")
	max, okM := headerInt(h, "x-rate-limit-api-token-max")
	interval, okI := headerInt(h, "x-rate-limit-api-token-interval-ms")
	if !okR || !okM || !okI || max <= 0 || interval <= 0 {
		return
	}
	var pace time.Duration
	// Only start pacing in the last tenth of the budget. Slowing down earlier
	// would penalize the common case, where a command uses a handful of calls.
	if remaining < max/10 {
		ratio := 1 - float64(remaining)/float64(max)
		pace = time.Duration(float64(interval) * ratio * float64(time.Millisecond))
	}
	c.paceMu.Lock()
	c.pace = pace
	c.paceMu.Unlock()
	if pace > 0 {
		c.log.Debug("pacing", "remaining", remaining, "max", max, "delay", pace)
	}
}

func (c *Client) awaitPace(ctx context.Context) error {
	c.paceMu.Lock()
	pace := c.pace
	c.paceMu.Unlock()
	if pace <= 0 {
		return nil
	}
	return c.sleep(ctx, pace)
}

func (c *Client) backoff(attempt int) time.Duration {
	base := time.Duration(1<<(attempt-1)) * 500 * time.Millisecond
	return c.jitter(base)
}

// isSerialRoute reports whether path is one of the three routes Trello gives a
// dedicated low limit.
//
// Nested resources under /1/members are explicitly exempt from that limit, so
// matching a bare prefix would throttle /1/members/me/boards, the hottest route
// in the whole tool, for no reason.
func isSerialRoute(path string) bool {
	p := strings.Trim(path, "/")
	if p == "search" || strings.HasPrefix(p, "search/") {
		return true
	}
	if p == "membersSearch" {
		return true
	}
	if p == "members" {
		return true
	}
	// /1/members/{id} is bare; /1/members/{id}/boards is nested and exempt.
	if rest, ok := strings.CutPrefix(p, "members/"); ok {
		return !strings.Contains(rest, "/")
	}
	return false
}

// parseAPIError extracts Trello's error code and message from a body that may
// or may not be JSON.
func parseAPIError(raw []byte) (code, message string) {
	trimmed := strings.TrimSpace(string(raw))
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &body) == nil && (body.Error != "" || body.Message != "") {
		message = body.Message
		if message == "" {
			message = body.Error
		}
		return body.Error, message
	}
	if trimmed == "" {
		return "", "no response body"
	}
	if len(trimmed) > 200 {
		trimmed = trimmed[:200]
	}
	return "", trimmed
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func headerInt(h http.Header, name string) (int, bool) {
	v := h.Get(name)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, false
	}
	return n, true
}

// redact strips credentials from anything derived from a request URL.
//
// Trello takes the key and token as query parameters, so a wrapped *url.Error
// carries both in its message. Every path that can reach a log or a user must
// go through this.
func redact(err error) string {
	msg := err.Error()
	var uerr *url.Error
	if errors.As(err, &uerr) {
		msg = strings.ReplaceAll(msg, uerr.URL, RedactURL(uerr.URL))
	}
	return msg
}

// RedactURL replaces the key and token query values with a placeholder.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<redacted url>"
	}
	q := u.Query()
	for _, k := range []string{"key", "token"} {
		if q.Get(k) != "" {
			q.Set(k, "REDACTED")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// defaultJitter spreads retries across a window so concurrent invocations of
// this binary do not synchronize into a burst.
func defaultJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d/2 + time.Duration(rand.Int64N(int64(d)))
}

// discardHandler drops log records when no logger was configured.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }
