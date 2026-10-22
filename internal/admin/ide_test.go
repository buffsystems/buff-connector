package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeIDE struct{ rows []DeveloperCalls }

func (f fakeIDE) DeveloperCalls() []DeveloperCalls { return f.rows }

// The operator sees who uses the company's tools from Buff Code (#368): each developer,
// how many calls, how many failed, and when last.
func TestTheIDESectionNamesTheDevelopersAndTheirCalls(t *testing.T) {
	at := time.Date(2026, 9, 22, 4, 53, 0, 0, time.UTC)
	s := New(filepath.Join(t.TempDir(), "c.yaml"), "test", nil).WithIDE(fakeIDE{rows: []DeveloperCalls{
		{Developer: "dev-alice", Calls: 5, Errors: 1, Last: at},
	}})
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/ide", nil)
	req.Header.Set("X-Buff-Token", os.Getenv(tokenEnv))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var got struct {
		Developers []struct {
			Developer string    `json:"developer"`
			Calls     int       `json:"calls"`
			Errors    int       `json:"errors"`
			Last      time.Time `json:"last"`
		} `json:"developers"`
	}
	_ = json.NewDecoder(res.Body).Decode(&got)
	if len(got.Developers) != 1 || got.Developers[0].Developer != "dev-alice" || got.Developers[0].Calls != 5 ||
		got.Developers[0].Errors != 1 || !got.Developers[0].Last.Equal(at) {
		t.Fatalf("ide = %+v", got)
	}
}

func TestWithoutTheAgentTheIDESectionIsEmpty(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "c.yaml"), "test", nil)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/ide", nil)
	req.Header.Set("X-Buff-Token", os.Getenv(tokenEnv))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var got map[string]any
	_ = json.NewDecoder(res.Body).Decode(&got)
	if list, ok := got["developers"].([]any); !ok || len(list) != 0 {
		t.Fatalf("ide = %v", got)
	}
}
