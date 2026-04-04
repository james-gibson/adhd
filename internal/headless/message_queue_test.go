package headless

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMessageQueueEnqueue(t *testing.T) {
	mq := NewMessageQueue(false, "", 10)

	// Enqueue a message
	entry := MCPTrafficLog{
		Timestamp: time.Now(),
		Type:      "request",
		Method:    "test.method",
	}
	mq.Enqueue(entry)

	// Check it was added
	logs := mq.GetBufferedLogs(nil, 0)
	if len(logs) != 1 {
		t.Errorf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Method != "test.method" {
		t.Errorf("expected method test.method, got %s", logs[0].Method)
	}
}

func TestMessageQueueOverflow(t *testing.T) {
	mq := NewMessageQueue(false, "", 3)

	// Enqueue more than maxSize messages
	for i := 0; i < 5; i++ {
		entry := MCPTrafficLog{
			Timestamp: time.Now(),
			Type:      "request",
			Method:    "test.method",
		}
		mq.Enqueue(entry)
	}

	// Check that only the last 3 are retained
	logs := mq.GetBufferedLogs(nil, 0)
	if len(logs) != 3 {
		t.Errorf("expected 3 logs after overflow, got %d", len(logs))
	}
}

func TestMessageQueueGetStats(t *testing.T) {
	mq := NewMessageQueue(true, "http://example.com", 100)
	mq.Enqueue(MCPTrafficLog{
		Timestamp: time.Now(),
		Type:      "request",
		Method:    "test.method",
	})

	stats := mq.GetStats()
	if stats.BufferedLogCount != 1 {
		t.Errorf("expected 1 buffered log, got %d", stats.BufferedLogCount)
	}
	if !stats.IsPrimePlus {
		t.Error("expected IsPrimePlus=true")
	}
	if stats.PrimeAddress != "http://example.com" {
		t.Errorf("expected prime address http://example.com, got %s", stats.PrimeAddress)
	}
	if stats.MaxBufferSize != 100 {
		t.Errorf("expected max buffer size 100, got %d", stats.MaxBufferSize)
	}
}

func TestMessageQueueFiltering(t *testing.T) {
	mq := NewMessageQueue(false, "", 10)

	now := time.Now()
	past := now.Add(-10 * time.Second)

	// Enqueue two messages with different timestamps
	mq.Enqueue(MCPTrafficLog{Timestamp: past, Type: "request"})
	mq.Enqueue(MCPTrafficLog{Timestamp: now, Type: "response"})

	// Get logs since past (should get both)
	logs := mq.GetBufferedLogs(&past, 0)
	if len(logs) != 2 {
		t.Errorf("expected 2 logs since past, got %d", len(logs))
	}

	// Get logs since now (should get only the second)
	logs = mq.GetBufferedLogs(&now, 0)
	if len(logs) != 1 {
		t.Errorf("expected 1 log since now, got %d", len(logs))
	}
}

func TestMessageQueueLimitResults(t *testing.T) {
	mq := NewMessageQueue(false, "", 10)

	// Enqueue 5 messages
	for i := 0; i < 5; i++ {
		mq.Enqueue(MCPTrafficLog{
			Timestamp: time.Now(),
			Type:      "request",
			Method:    "test",
		})
	}

	// Get only 2 results
	logs := mq.GetBufferedLogs(nil, 2)
	if len(logs) != 2 {
		t.Errorf("expected 2 logs with limit=2, got %d", len(logs))
	}
}

func TestMessageQueuePushToPrime(t *testing.T) {
	// Create a mock smoke-alarm server that expects push-logs call
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		// Verify it's the push-logs method
		method, ok := rpcReq["method"].(string)
		if !ok || method != "smoke-alarm.isotope.push-logs" {
			t.Errorf("expected method smoke-alarm.isotope.push-logs, got %v", method)
		}

		// Return successful response
		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]interface{}{
				"success": true,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create prime-plus queue with mock server
	mq := NewMessageQueue(true, server.URL, 10)
	mq.Enqueue(MCPTrafficLog{
		Timestamp: time.Now(),
		Type:      "request",
		Method:    "test.method",
	})

	// Push to prime should succeed
	err := mq.PushToPrime()
	if err != nil {
		t.Errorf("PushToPrime failed: %v", err)
	}

	// Buffer should be cleared after successful push
	stats := mq.GetStats()
	if stats.BufferedLogCount != 0 {
		t.Errorf("expected 0 logs after push, got %d", stats.BufferedLogCount)
	}
}

func TestMessageQueuePushToNonPrimePlus(t *testing.T) {
	// Non-prime-plus should not push
	mq := NewMessageQueue(false, "", 10)
	mq.Enqueue(MCPTrafficLog{
		Timestamp: time.Now(),
		Type:      "request",
	})

	// Push should be no-op and succeed
	err := mq.PushToPrime()
	if err != nil {
		t.Errorf("PushToPrime should be no-op for non-prime-plus: %v", err)
	}

	// Buffer should still have the message
	stats := mq.GetStats()
	if stats.BufferedLogCount != 1 {
		t.Errorf("expected 1 log in buffer for non-prime-plus, got %d", stats.BufferedLogCount)
	}
}

func TestMessageQueuePushToEmptyBuffer(t *testing.T) {
	// Create a mock server that should NOT be called
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	mq := NewMessageQueue(true, server.URL, 10)

	// Push with empty buffer should succeed without calling server
	err := mq.PushToPrime()
	if err != nil {
		t.Errorf("PushToPrime with empty buffer failed: %v", err)
	}

	if called {
		t.Error("server should not be called when buffer is empty")
	}
}

func TestMessageQueuePushFailure(t *testing.T) {
	// Create a mock server that returns an error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"error": map[string]interface{}{
				"code":    -32600,
				"message": "Invalid request",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	mq := NewMessageQueue(true, server.URL, 10)
	mq.Enqueue(MCPTrafficLog{
		Timestamp: time.Now(),
		Type:      "request",
	})

	// Push should fail
	err := mq.PushToPrime()
	if err == nil {
		t.Error("PushToPrime should fail when server returns error")
	}

	// Buffer should still have the message (not cleared on failure)
	stats := mq.GetStats()
	if stats.BufferedLogCount != 1 {
		t.Errorf("expected 1 log in buffer after failed push, got %d", stats.BufferedLogCount)
	}
}

func TestMessageQueueClose(t *testing.T) {
	mq := NewMessageQueue(true, "", 10)
	mq.Enqueue(MCPTrafficLog{
		Timestamp: time.Now(),
		Type:      "request",
	})

	// Close should succeed
	err := mq.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// Retry ticker should be stopped (can't explicitly test, but shouldn't panic)
}
