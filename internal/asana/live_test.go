package asana

import (
	"os"
	"testing"
	"time"
)

// TestLiveListUsersAndProjects talks to the real Asana API. It runs only
// when ASANA_LIVE_TEST=1 and credentials are in the environment:
//
//	ASANA_LIVE_TEST=1 ASANA_ACCESS_TOKEN=... ASANA_WORKSPACE_GID=... go test ./internal/asana -run TestLive -v
//
// It logs only counts, never names, emails or the token.
func TestLiveListUsersAndProjects(t *testing.T) {
	if os.Getenv("ASANA_LIVE_TEST") != "1" {
		t.Skip("set ASANA_LIVE_TEST=1 to run against the real Asana API")
	}
	token, ws := os.Getenv("ASANA_ACCESS_TOKEN"), os.Getenv("ASANA_WORKSPACE_GID")
	if token == "" || ws == "" {
		t.Fatal("ASANA_ACCESS_TOKEN and ASANA_WORKSPACE_GID are required")
	}
	c := New(Options{BaseURL: "https://app.asana.com/api/1.0", Token: token, RateLimitPerMinute: 60, MaxRetries: 3, HTTPTimeout: 30 * time.Second})

	var users, withEmail int
	if err := c.ListUsers(t.Context(), ws, func(p []User) error {
		for _, u := range p {
			users++
			if u.GID == "" {
				t.Error("user without gid")
			}
			if u.Email != nil {
				withEmail++
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var projects, archived int
	if err := c.ListProjects(t.Context(), ws, func(p []Project) error {
		for _, pr := range p {
			projects++
			if pr.Archived {
				archived++
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("users=%d (with email=%d) projects=%d (archived=%d) retries=%d", users, withEmail, projects, archived, c.Retries())
	if users < 3 || projects < 2 {
		t.Errorf("exercise setup expects >=3 users and >=2 projects visible to the token")
	}
}
