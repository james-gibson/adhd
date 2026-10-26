package mcpserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// prRepoPattern keeps the caller from turning the cache URL into an arbitrary
// target. The cache URL itself is local config (ADHD_PR_CACHE_URL), never a
// request field.
var prRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

const maxPRBody = 16 << 20

// handlePRCache reads a pull request through the lezz PR cache. It is
// read-only, has no caller-supplied endpoint, and is safe to expose on the
// unauthenticated layer (see #27).
func (s *Server) handlePRCache(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
	m, ok := params.(map[string]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "params must be an object"}
	}
	repo, _ := m["repo"].(string)
	if !prRepoPattern.MatchString(repo) {
		return nil, &jsonrpcError{Code: -32602, Message: "repo must be owner/name"}
	}
	number, ok := asInt(m["number"])
	if !ok || number <= 0 {
		return nil, &jsonrpcError{Code: -32602, Message: "number must be a positive integer"}
	}
	kind := "view"
	if v, ok := m["kind"].(string); ok && v != "" {
		kind = v
	}
	if kind != "view" && kind != "diff" {
		return nil, &jsonrpcError{Code: -32602, Message: "kind must be view or diff"}
	}
	refresh, _ := m["refresh"].(bool)

	cacheURL := strings.TrimRight(strings.TrimSpace(os.Getenv("ADHD_PR_CACHE_URL")), "/")
	if cacheURL == "" {
		return nil, &jsonrpcError{
			Code:    -32000,
			Message: "pr cache is not configured: run `lezz cache` and set ADHD_PR_CACHE_URL",
		}
	}
	u, err := url.Parse(cacheURL + "/pr")
	if err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: "pr cache URL: " + err.Error()}
	}
	q := u.Query()
	q.Set("repo", repo)
	q.Set("number", strconv.Itoa(number))
	q.Set("kind", kind)
	if refresh {
		q.Set("refresh", "1")
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: "pr cache request: " + err.Error()}
	}
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: "pr cache unreachable: " + err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPRBody))
	if err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: "pr cache read: " + err.Error()}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &jsonrpcError{Code: -32000, Message: fmt.Sprintf("pr cache: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))}
	}
	return map[string]interface{}{
		"body":       string(body),
		"kind":       kind,
		"source":     resp.Header.Get("X-Cache"),
		"stale":      resp.Header.Get("X-Stale") == "true",
		"fetched_at": resp.Header.Get("X-Fetched-At"),
		"bytes":      len(body),
	}, nil
}

func asInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		i := int(n)
		return i, float64(i) == n
	case int:
		return n, true
	case int64:
		return int(n), true
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	default:
		return 0, false
	}
}
