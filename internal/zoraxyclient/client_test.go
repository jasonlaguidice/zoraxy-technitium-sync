package zoraxyclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing test server url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parsing test server port: %v", err)
	}
	c := New(port, "test-api-key")
	return c, srv.Close
}

func TestListProxyHosts_ParsesResponseAndAuthHeader(t *testing.T) {
	c, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/plugin/api/proxy/list" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("type") != "host" {
			t.Errorf("expected type=host query param, got %q", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Errorf("expected bearer auth header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"RootOrMatchingDomain":"app.example.com","MatchingDomainAlias":["alt.example.com"],"Disabled":false},
			{"RootOrMatchingDomain":"disabled.example.com","MatchingDomainAlias":[],"Disabled":true}
		]`))
	})
	defer closeFn()

	hosts, err := c.ListProxyHosts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("expected 2 hosts, got %d", len(hosts))
	}
	if hosts[0].RootOrMatchingDomain != "app.example.com" || len(hosts[0].MatchingDomainAlias) != 1 {
		t.Fatalf("unexpected first host: %+v", hosts[0])
	}
	if !hosts[1].Disabled {
		t.Fatalf("expected second host to be disabled: %+v", hosts[1])
	}
}

func TestListProxyHosts_NonOKStatusIsError(t *testing.T) {
	c, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	defer closeFn()

	if _, err := c.ListProxyHosts(context.Background()); err == nil {
		t.Fatalf("expected error for non-200 status")
	}
}

func TestDeriveDesiredHostnames(t *testing.T) {
	hosts := []ProxyHost{
		{RootOrMatchingDomain: "App.Example.com", MatchingDomainAlias: []string{"alt.example.com", "  "}},
		{RootOrMatchingDomain: "disabled.example.com", Disabled: true},
		{RootOrMatchingDomain: "app.example.com"}, // duplicate after lowering
	}
	got := DeriveDesiredHostnames(hosts)
	want := []string{"alt.example.com", "app.example.com"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestListDesiredHosts_EndToEnd(t *testing.T) {
	c, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"RootOrMatchingDomain":"svc.example.com","MatchingDomainAlias":null,"Disabled":false}]`))
	})
	defer closeFn()

	got, err := c.ListDesiredHosts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "svc.example.com" {
		t.Fatalf("unexpected result: %v", got)
	}
}
