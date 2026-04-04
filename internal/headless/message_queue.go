package headless

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/james-gibson/adhd/internal/mcpclient"
)

// MessageQueue buffers JSONL logs and handles delivery to prime smoke-alarm
// Follows the pattern: if prime is unavailable, queue locally and retry
type MessageQueue struct {
	mu          sync.RWMutex
	buffer      []MCPTrafficLog
	maxSize     int
	isPrimePlus bool // true if this alarm is prime-plus (secondary)
	primeAddr   string
	retryTicker *time.Ticker
	doneCh      chan struct{}
}

// NewMessageQueue creates a buffering queue for log messages
func NewMessageQueue(isPrimePlus bool, primeAddr string, maxBufferSize int) *MessageQueue {
	mq := &MessageQueue{
		buffer:      make([]MCPTrafficLog, 0, maxBufferSize),
		maxSize:     maxBufferSize,
		isPrimePlus: isPrimePlus,
		primeAddr:   primeAddr,
		doneCh:      make(chan struct{}),
	}

	// If prime-plus, start retry goroutine to push queued messages to prime
	if isPrimePlus && primeAddr != "" {
		mq.retryTicker = time.NewTicker(5 * time.Second)
		go mq.retryLoop()
	}

	return mq
}

// Enqueue adds a message to the buffer
// If buffer is full, oldest messages are dropped
func (mq *MessageQueue) Enqueue(entry MCPTrafficLog) {
	mq.mu.Lock()
	defer mq.mu.Unlock()

	// If buffer is at capacity, remove oldest
	if len(mq.buffer) >= mq.maxSize {
		mq.buffer = mq.buffer[1:]
		slog.Debug("message queue at capacity, dropped oldest message")
	}

	mq.buffer = append(mq.buffer, entry)
	slog.Debug("enqueued traffic log", "queue_size", len(mq.buffer))
}

// GetBufferedLogs returns all currently buffered logs
// Used when prime queries this isotope for its logs
func (mq *MessageQueue) GetBufferedLogs(since *time.Time, limit int) []MCPTrafficLog {
	mq.mu.RLock()
	defer mq.mu.RUnlock()

	var results []MCPTrafficLog

	for _, entry := range mq.buffer {
		// Filter by timestamp if since is provided
		if since != nil && entry.Timestamp.Before(*since) {
			continue
		}

		results = append(results, entry)

		// Limit results if specified
		if limit > 0 && len(results) >= limit {
			break
		}
	}

	return results
}

// PushToPrime attempts to send buffered logs to prime smoke-alarm via MCP
// Returns error if push failed (will retry later)
func (mq *MessageQueue) PushToPrime() error {
	if !mq.isPrimePlus || mq.primeAddr == "" {
		return nil // Not prime-plus, nothing to push
	}

	mq.mu.RLock()
	logsToSend := make([]MCPTrafficLog, len(mq.buffer))
	copy(logsToSend, mq.buffer)
	mq.mu.RUnlock()

	if len(logsToSend) == 0 {
		return nil // Nothing to send
	}

	// Create MCP client to communicate with prime
	client := mcpclient.NewHTTPClient(mq.primeAddr, 10*time.Second)

	// Call smoke-alarm.isotope.push-logs via MCP
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Call(ctx, "smoke-alarm.isotope.push-logs", map[string]interface{}{
		"logs":      logsToSend,
		"timestamp": time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("failed to send push-logs to prime: %w", err)
	}

	// Check for JSON-RPC error
	if resp.Error != nil {
		return fmt.Errorf("push-logs error from prime: code=%d message=%s", resp.Error.Code, resp.Error.Message)
	}

	slog.Info("successfully pushed logs to prime",
		"prime_addr", mq.primeAddr,
		"log_count", len(logsToSend),
	)

	// On success, clear the buffer
	mq.mu.Lock()
	mq.buffer = make([]MCPTrafficLog, 0, mq.maxSize)
	mq.mu.Unlock()

	return nil
}

// retryLoop periodically attempts to push queued messages to prime
func (mq *MessageQueue) retryLoop() {
	defer mq.retryTicker.Stop()

	for {
		select {
		case <-mq.retryTicker.C:
			// Attempt to push any queued logs to prime
			if err := mq.PushToPrime(); err != nil {
				slog.Warn("failed to push logs to prime, will retry",
					"error", err,
					"prime_addr", mq.primeAddr,
				)
			}

		case <-mq.doneCh:
			return
		}
	}
}

// Close stops the retry loop and gracefully shuts down
func (mq *MessageQueue) Close() error {
	if mq.isPrimePlus && mq.primeAddr != "" {
		// Final attempt to push remaining logs before shutdown
		if err := mq.PushToPrime(); err != nil {
			slog.Warn("final push to prime failed during shutdown", "error", err)
		}
		close(mq.doneCh)
	}
	return nil
}

// Stats returns current queue statistics
type QueueStats struct {
	BufferedLogCount int
	IsPrimePlus      bool
	PrimeAddress     string
	MaxBufferSize    int
}

// GetStats returns current queue statistics
func (mq *MessageQueue) GetStats() QueueStats {
	mq.mu.RLock()
	defer mq.mu.RUnlock()

	return QueueStats{
		BufferedLogCount: len(mq.buffer),
		IsPrimePlus:      mq.isPrimePlus,
		PrimeAddress:     mq.primeAddr,
		MaxBufferSize:    mq.maxSize,
	}
}
