package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// ghQueueURL is the opt-in switch for routing adhd.gh.run through a lezz-owned
// queue. Unset means the historical direct `gh` subprocess path.
func ghQueueURL() string {
	return strings.TrimSpace(os.Getenv("ADHD_GH_QUEUE_URL"))
}

type ghQueueEnqueue struct {
	Args           []string `json:"args"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	Wait           bool     `json:"wait"`
}

type ghQueueResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
	Success  bool   `json:"success"`
	Error    string `json:"error"`
	ID       string `json:"id"`
}

// runGHViaQueue enqueues the gh invocation and blocks for its completion. The
// queue owns the process and the rate limit; adhd keeps the same response shape
// as the direct path.
func (s *Server) runGHViaQueue(ctx context.Context, queueURL string, args []string, timeoutSec int) (interface{}, *jsonrpcError) {
	body, err := json.Marshal(ghQueueEnqueue{Args: args, TimeoutSeconds: timeoutSec, Wait: true})
	if err != nil {
		return nil, &jsonrpcError{Code: -32603, Message: "encode queue request: " + err.Error()}
	}
	url := strings.TrimRight(queueURL, "/") + "/enqueue"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, &jsonrpcError{Code: -32603, Message: "build queue request: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: time.Duration(timeoutSec+10) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: "gh queue unreachable: " + err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	var res ghQueueResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: fmt.Sprintf("gh queue returned %d with invalid body", resp.StatusCode)}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &jsonrpcError{Code: -32000, Message: fmt.Sprintf("gh queue: %s", res.Error)}
	}

	return map[string]interface{}{
		"output":    res.Output,
		"exit_code": res.ExitCode,
		"success":   res.Success,
		"queued":    true,
		"queue_id":  res.ID,
	}, nil
}
