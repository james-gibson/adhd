package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeRegistry serves GET /isotope/list from a mutable record set, so a test
// can move a name the way a restarted surface does.
func fakeRegistry(t *testing.T, records *[]Record) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/isotope/list" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(*records); err != nil {
			t.Errorf("encode records: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestResolveReturnsTheRegisteredEndpointAsAProbeURL(t *testing.T) {
	records := []Record{
		{Name: "adhd", Endpoint: "127.0.0.1:9090"},
		{Name: "workspace-42", Endpoint: "127.0.0.1:64194"},
	}
	srv := fakeRegistry(t, &records)

	got, err := NewClient(srv.URL, 0).Resolve(context.Background(), "workspace-42")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := "http://127.0.0.1:64194/mcp"; got != want {
		t.Errorf("resolved endpoint = %q, want %q", got, want)
	}
}

func TestResolveFollowsTheNameRatherThanCachingTheAddress(t *testing.T) {
	records := []Record{{Name: "workspace-42", Endpoint: "127.0.0.1:64194"}}
	srv := fakeRegistry(t, &records)
	client := NewClient(srv.URL, 0)

	first, err := client.Resolve(context.Background(), "workspace-42")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	// The session restarts on a new ephemeral port and re-registers.
	records[0].Endpoint = "127.0.0.1:64210"

	second, err := client.Resolve(context.Background(), "workspace-42")
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}

	if first != "http://127.0.0.1:64194/mcp" {
		t.Errorf("first resolve = %q", first)
	}
	if second != "http://127.0.0.1:64210/mcp" {
		t.Errorf("second resolve = %q, want the moved endpoint", second)
	}
}

func TestResolveReportsAMissingNameAsNotFound(t *testing.T) {
	records := []Record{{Name: "adhd", Endpoint: "127.0.0.1:9090"}}
	srv := fakeRegistry(t, &records)

	_, err := NewClient(srv.URL, 0).Resolve(context.Background(), "workspace-42")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestResolveDistinguishesAnUnreachableRegistryFromAMissingName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, 0).Resolve(context.Background(), "workspace-42")
	if err == nil {
		t.Fatal("want an error from a failing registry")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("a failing registry is not a missing name: %v", err)
	}
}

func TestResolveWithoutARegistrySaysSo(t *testing.T) {
	_, err := NewClient("", 0).Resolve(context.Background(), "workspace-42")
	if err == nil {
		t.Fatal("want an error when no registry is configured")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("an unconfigured registry is not a missing name: %v", err)
	}
}

func TestProbeURL(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     string
	}{
		{name: "bare host:port, as the lab registers a surface", endpoint: "127.0.0.1:64194", want: "http://127.0.0.1:64194/mcp"},
		{name: "host:port with no scheme or path", endpoint: "localhost:9090", want: "http://localhost:9090/mcp"},
		{name: "scheme without a path", endpoint: "http://127.0.0.1:64194", want: "http://127.0.0.1:64194/mcp"},
		{name: "an explicit path is kept", endpoint: "http://127.0.0.1:64194/custom", want: "http://127.0.0.1:64194/custom"},
		{name: "https is kept", endpoint: "https://api.example.com/mcp", want: "https://api.example.com/mcp"},
		{name: "empty stays empty", endpoint: "  ", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProbeURL(tc.endpoint); got != tc.want {
				t.Errorf("ProbeURL(%q) = %q, want %q", tc.endpoint, got, tc.want)
			}
		})
	}
}
