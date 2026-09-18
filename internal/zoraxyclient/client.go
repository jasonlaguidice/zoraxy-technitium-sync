// Package zoraxyclient reads Zoraxy's own configured HTTP proxy host rules
// via its local plugin API, and derives the set of hostnames that should
// have DNS records from them. It knows nothing about Technitium or
// reconciliation.
package zoraxyclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ProxyHost mirrors the fields we need from Zoraxy's ProxyEndpoint struct
// (reference/zoraxy/src/mod/dynamicproxy/typedef.go). Zoraxy does not put
// json tags on that struct, so its JSON keys are the bare Go field names;
// these field names must match exactly for encoding/json to populate them.
type ProxyHost struct {
	RootOrMatchingDomain string
	MatchingDomainAlias  []string
	Disabled             bool
}

const listHostsPath = "/plugin/api/proxy/list?type=host"

// Client fetches Zoraxy's proxy host list over its local plugin API.
type Client struct {
	ZoraxyPort int
	APIKey     string
	HTTPClient *http.Client
}

func New(zoraxyPort int, apiKey string) *Client {
	return &Client{
		ZoraxyPort: zoraxyPort,
		APIKey:     apiKey,
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// ListProxyHosts calls GET /plugin/api/proxy/list?type=host on Zoraxy's own
// local API, authenticated with the API key handed to this plugin at
// configure time.
func (c *Client) ListProxyHosts(ctx context.Context) ([]ProxyHost, error) {
	url := fmt.Sprintf("http://localhost:%d%s", c.ZoraxyPort, listHostsPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling zoraxy proxy list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zoraxy proxy list returned status %d", resp.StatusCode)
	}

	var hosts []ProxyHost
	if err := json.NewDecoder(resp.Body).Decode(&hosts); err != nil {
		return nil, fmt.Errorf("decoding zoraxy proxy list: %w", err)
	}
	return hosts, nil
}

// DeriveDesiredHostnames extracts every hostname that should have a DNS
// record from a list of Zoraxy proxy host rules: the root domain plus every
// alias of each rule that isn't disabled. The result is deduplicated,
// lowercased and sorted for determinism.
func DeriveDesiredHostnames(hosts []ProxyHost) []string {
	set := map[string]bool{}
	for _, h := range hosts {
		if h.Disabled {
			continue
		}
		add := func(domain string) {
			domain = strings.ToLower(strings.TrimSpace(domain))
			if domain != "" {
				set[domain] = true
			}
		}
		add(h.RootOrMatchingDomain)
		for _, alias := range h.MatchingDomainAlias {
			add(alias)
		}
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// ListDesiredHosts implements reconciler.HostLister.
func (c *Client) ListDesiredHosts(ctx context.Context) ([]string, error) {
	hosts, err := c.ListProxyHosts(ctx)
	if err != nil {
		return nil, err
	}
	return DeriveDesiredHostnames(hosts), nil
}
