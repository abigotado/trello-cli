package trello

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/abigotado/trello-cli/internal/errx"
)

// recorder captures what the client actually sent, so each mutation is
// asserted on its verb, path, and parameters rather than on its return value.
type recorder struct {
	method string
	path   string
	query  url.Values
	calls  int
}

func recordingServer(t *testing.T, body string) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.calls++
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.query = r.URL.Query()
		if body == "" {
			body = "{}"
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func TestMutationsSendTheRightRequest(t *testing.T) {
	name := "Ship it"
	due := ""
	closed := true
	listID := "list-9"

	tests := []struct {
		name       string
		body       string
		call       func(*Client) error
		wantMethod string
		wantPath   string
		wantQuery  map[string]string
	}{
		{
			name:       "create list",
			call:       func(c *Client) error { _, err := c.CreateList(context.Background(), "b1", "Doing", "top"); return err },
			wantMethod: http.MethodPost, wantPath: "/lists",
			wantQuery: map[string]string{"idBoard": "b1", "name": "Doing", "pos": "top"},
		},
		{
			name:       "archive list",
			call:       func(c *Client) error { _, err := c.SetListClosed(context.Background(), "l1", true); return err },
			wantMethod: http.MethodPut, wantPath: "/lists/l1/closed",
			wantQuery: map[string]string{"value": "true"},
		},
		{
			name: "create card",
			call: func(c *Client) error {
				_, err := c.CreateCard(context.Background(), "l1", CardInput{Name: &name})
				return err
			},
			wantMethod: http.MethodPost, wantPath: "/cards",
			wantQuery: map[string]string{"idList": "l1", "name": "Ship it"},
		},
		{
			// An empty string is how Trello clears a due date, so the pointer
			// must survive as a present-but-empty parameter.
			name: "clear due sends an empty value",
			call: func(c *Client) error {
				_, err := c.UpdateCard(context.Background(), "c1", CardInput{Due: &due})
				return err
			},
			wantMethod: http.MethodPut, wantPath: "/cards/c1",
			wantQuery: map[string]string{"due": ""},
		},
		{
			name: "move card",
			call: func(c *Client) error {
				_, err := c.UpdateCard(context.Background(), "c1", CardInput{IDList: &listID})
				return err
			},
			wantMethod: http.MethodPut, wantPath: "/cards/c1",
			wantQuery: map[string]string{"idList": "list-9"},
		},
		{
			name: "archive card",
			call: func(c *Client) error {
				_, err := c.UpdateCard(context.Background(), "c1", CardInput{Closed: &closed})
				return err
			},
			wantMethod: http.MethodPut, wantPath: "/cards/c1",
			wantQuery: map[string]string{"closed": "true"},
		},
		{
			name:       "delete card",
			call:       func(c *Client) error { return c.DeleteCard(context.Background(), "c1") },
			wantMethod: http.MethodDelete, wantPath: "/cards/c1",
		},
		{
			name:       "add label",
			call:       func(c *Client) error { return c.AddLabel(context.Background(), "c1", "lab1") },
			wantMethod: http.MethodPost, wantPath: "/cards/c1/idLabels",
			wantQuery: map[string]string{"value": "lab1"},
		},
		{
			name:       "remove label",
			call:       func(c *Client) error { return c.RemoveLabel(context.Background(), "c1", "lab1") },
			wantMethod: http.MethodDelete, wantPath: "/cards/c1/idLabels/lab1",
		},
		{
			name:       "assign member",
			call:       func(c *Client) error { return c.AssignMember(context.Background(), "c1", "m1") },
			wantMethod: http.MethodPost, wantPath: "/cards/c1/idMembers",
			wantQuery: map[string]string{"value": "m1"},
		},
		{
			name:       "unassign member",
			call:       func(c *Client) error { return c.UnassignMember(context.Background(), "c1", "m1") },
			wantMethod: http.MethodDelete, wantPath: "/cards/c1/idMembers/m1",
		},
		{
			name:       "add comment",
			body:       `{"id":"a1","date":"2026-01-01T00:00:00Z","data":{"text":"hi"},"memberCreator":{"username":"nik"}}`,
			call:       func(c *Client) error { _, err := c.AddComment(context.Background(), "c1", "hi"); return err },
			wantMethod: http.MethodPost, wantPath: "/cards/c1/actions/comments",
			wantQuery: map[string]string{"text": "hi"},
		},
		{
			name:       "create checklist",
			call:       func(c *Client) error { _, err := c.CreateChecklist(context.Background(), "c1", "Steps"); return err },
			wantMethod: http.MethodPost, wantPath: "/checklists",
			wantQuery: map[string]string{"idCard": "c1", "name": "Steps"},
		},
		{
			name: "add check item",
			call: func(c *Client) error {
				_, err := c.AddCheckItem(context.Background(), "cl1", "Step 1", false)
				return err
			},
			wantMethod: http.MethodPost, wantPath: "/checklists/cl1/checkItems",
			wantQuery: map[string]string{"name": "Step 1", "checked": "false"},
		},
		{
			// Trello exposes item state only through the card route.
			name: "toggle check item goes through the card",
			call: func(c *Client) error {
				_, err := c.SetCheckItemState(context.Background(), "c1", "i1", true)
				return err
			},
			wantMethod: http.MethodPut, wantPath: "/cards/c1/checkItem/i1",
			wantQuery: map[string]string{"state": "complete"},
		},
		{
			name: "add attachment",
			call: func(c *Client) error {
				_, err := c.AddAttachment(context.Background(), "c1", "https://example.com", "Spec")
				return err
			},
			wantMethod: http.MethodPost, wantPath: "/cards/c1/attachments",
			wantQuery: map[string]string{"url": "https://example.com", "name": "Spec"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, rec := recordingServer(t, tt.body)
			c := New(srv.URL, Credentials{APIKey: "k", Token: "t"}, 1, WithHTTPClient(srv.Client()))

			if err := tt.call(c); err != nil {
				t.Fatalf("call error = %v", err)
			}
			if rec.method != tt.wantMethod {
				t.Errorf("method = %s, want %s", rec.method, tt.wantMethod)
			}
			if rec.path != tt.wantPath {
				t.Errorf("path = %s, want %s", rec.path, tt.wantPath)
			}
			for k, want := range tt.wantQuery {
				if !rec.query.Has(k) {
					t.Errorf("query is missing %q", k)
					continue
				}
				if got := rec.query.Get(k); got != want {
					t.Errorf("query %q = %q, want %q", k, got, want)
				}
			}
			// Credentials ride along on every request.
			if rec.query.Get("key") != "k" || rec.query.Get("token") != "t" {
				t.Error("credentials were not attached")
			}
		})
	}
}

// An unmentioned field must not be sent at all, or an update would silently
// blank out everything the caller did not name.
func TestUpdateSendsOnlySuppliedFields(t *testing.T) {
	srv, rec := recordingServer(t, "{}")
	c := New(srv.URL, Credentials{APIKey: "k", Token: "t"}, 1, WithHTTPClient(srv.Client()))

	name := "Renamed"
	if _, err := c.UpdateCard(context.Background(), "c1", CardInput{Name: &name}); err != nil {
		t.Fatalf("UpdateCard() error = %v", err)
	}
	for _, absent := range []string{"desc", "due", "dueComplete", "idList", "closed", "pos"} {
		if rec.query.Has(absent) {
			t.Errorf("unsupplied field %q was sent as %q", absent, rec.query.Get(absent))
		}
	}
	if rec.query.Get("name") != "Renamed" {
		t.Errorf("name = %q", rec.query.Get("name"))
	}
}

// A transport or server failure leaves it unknown whether a POST took effect.
// Replaying it could create a second card, and a duplicate is worse than a
// reported failure the caller can retry deliberately.
func TestPostIsNotReplayedWhenItMayHaveApplied(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantCalls int
	}{
		{"500 does not replay a POST", http.StatusInternalServerError, 1},
		// A 429 was refused before the server acted, so replaying is safe.
		{"429 does replay a POST", http.StatusTooManyRequests, maxAttempts},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"message":"nope"}`))
			}))
			defer srv.Close()

			c := New(srv.URL, Credentials{APIKey: "k", Token: "t"}, 1,
				WithHTTPClient(srv.Client()),
				WithSleep(func(context.Context, time.Duration) error { return nil }),
				WithJitter(func(d time.Duration) time.Duration { return d }),
			)
			if _, err := c.CreateCard(context.Background(), "l1", CardInput{}); err == nil {
				t.Fatal("expected an error")
			}
			if calls != tt.wantCalls {
				t.Errorf("server saw %d calls, want %d", calls, tt.wantCalls)
			}
		})
	}
}

// PUT and DELETE repeat harmlessly, so a 5xx is still worth retrying there.
func TestIdempotentVerbsStillRetryOnServerError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, Credentials{APIKey: "k", Token: "t"}, 1,
		WithHTTPClient(srv.Client()),
		WithSleep(func(context.Context, time.Duration) error { return nil }),
		WithJitter(func(d time.Duration) time.Duration { return d }),
	)
	if err := c.DeleteCard(context.Background(), "c1"); errx.ExitCode(err) != errx.CodeRetryable {
		t.Errorf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeRetryable)
	}
	if calls != maxAttempts {
		t.Errorf("server saw %d calls, want %d", calls, maxAttempts)
	}
}

func TestIsIdempotent(t *testing.T) {
	tests := []struct {
		method string
		want   bool
	}{
		{http.MethodGet, true},
		{http.MethodPut, true},
		{http.MethodDelete, true},
		{http.MethodPost, false},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			if got := isIdempotent(tt.method); got != tt.want {
				t.Errorf("isIdempotent(%s) = %v, want %v", tt.method, got, tt.want)
			}
		})
	}
}

// The response body is not Trello's to control: a proxy, WAF, or gateway in
// the path may echo the request URI, and Trello takes the key and token as
// query parameters. Without scrubbing, an nginx or Cloudflare 403 page puts
// live credentials into error.message, which is printed on stdout.
func TestErrorBodyCannotLeakCredentials(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"proxy 403 echoing the uri", http.StatusForbidden, "403 Forbidden: /members/me?key=SECRETKEY&token=SECRETTOKEN"},
		{"gateway 502 echoing the uri", http.StatusBadGateway, "upstream failed for /1/cards?key=SECRETKEY&token=SECRETTOKEN"},
		{"json error echoing the uri", http.StatusBadRequest, `{"message":"bad request for /1/cards?key=SECRETKEY&token=SECRETTOKEN"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c := New(srv.URL, Credentials{APIKey: "SECRETKEY", Token: "SECRETTOKEN"}, 1,
				WithHTTPClient(srv.Client()),
				WithSleep(func(context.Context, time.Duration) error { return nil }),
				WithJitter(func(d time.Duration) time.Duration { return d }),
			)
			_, err := c.Me(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, secret := range []string{"SECRETKEY", "SECRETTOKEN"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error message leaked %s:\n%s", secret, err.Error())
				}
			}
		})
	}
}

func TestScrubSecrets(t *testing.T) {
	tests := []struct{ in, want string }{
		{"key=abc&token=def", "key=REDACTED&token=REDACTED"},
		{"?fields=id&key=abc", "?fields=id&key=REDACTED"},
		{"TOKEN=abc", "TOKEN=REDACTED"},
		{"no secrets here", "no secrets here"},
		// A field merely ending in "key" is not a credential parameter.
		{"idList=abc", "idList=abc"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := ScrubSecrets(tt.in); got != tt.want {
				t.Errorf("ScrubSecrets(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
