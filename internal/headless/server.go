package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpserver"
	"github.com/james-gibson/adhd/internal/smokelink"
)

// Server runs ADHD in headless mode, logging all MCP traffic as JSONL
type Server struct {
	cfg          *config.Config
	cluster      *lights.Cluster
	mcpServer    *mcpserver.Server
	smokeWatcher *smokelink.Watcher
	logFile      *os.File
	msgQueue     *MessageQueue
	isPrimePlus  bool
	role         IsotopeRole
	primeAddr    string
	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
}

// MCPTrafficLog represents a single MCP request/response for JSONL output.
type MCPTrafficLog struct {
	Timestamp time.Time               `json:"timestamp"`
	Type      string                  `json:"type"` // "request", "response", or "light-update"
	Method    string                  `json:"method,omitempty"`
	Params    map[string]interface{}  `json:"params,omitempty"`
	Result    map[string]interface{}  `json:"result,omitempty"`
	Error     *map[string]interface{} `json:"error,omitempty"`
	Latency   float64                 `json:"latency_ms,omitempty"`

	// Light-update fields (populated when Type == "light-update")
	TargetID  string `json:"target_id,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	OldStatus string `json:"old_status,omitempty"`
	NewStatus string `json:"new_status,omitempty"`
	Via       string `json:"via,omitempty"` // "poll" or "sse"
}

// New creates a headless ADHD server
func New(cfg *config.Config) *Server {
	ctx, cancel := context.WithCancel(context.Background())

	cluster := lights.NewCluster()

	// Initialize basic lights from config
	for _, bin := range cfg.Features.Binaries {
		for _, feature := range bin.Features {
			light := lights.New(feature.Name, "feature")
			light.Source = bin.Name
			light.Status = lights.StatusGreen
			light.SourceMeta = map[string]string{
				"binary":   bin.Name,
				"endpoint": bin.Endpoint,
			}
			cluster.Add(light)
		}
	}

	return &Server{
		cfg:     cfg,
		cluster: cluster,
		ctx:     ctx,
		cancel:  cancel,
		role:    RoleStandalone, // Default role until configured
	}
}

// SetupMessageQueue initializes message queueing for prime-plus topology
func (s *Server) SetupMessageQueue(isPrimePlus bool, primeAddr string, maxBufferSize int) {
	s.isPrimePlus = isPrimePlus
	s.primeAddr = primeAddr
	s.msgQueue = NewMessageQueue(isPrimePlus, primeAddr, maxBufferSize)
	if isPrimePlus {
		s.role = RolePrimePlus
	}
}

// SetRole explicitly sets the topology role (for testing or explicit configuration)
func (s *Server) SetRole(role IsotopeRole) {
	s.role = role
}

// GetRole returns the current topology role
func (s *Server) GetRole() IsotopeRole {
	return s.role
}

// SetPrimeAddr sets the prime address (used by auto-discovery)
func (s *Server) SetPrimeAddr(addr string) {
	s.primeAddr = addr
	// If we have a message queue but no prime addr was set, configure it now
	if s.msgQueue != nil && s.isPrimePlus && addr != "" {
		// Create new queue with discovered prime address
		s.msgQueue = NewMessageQueue(true, addr, s.msgQueue.GetStats().MaxBufferSize)
	}
}

// GetPrimeAddr returns the currently configured prime address
func (s *Server) GetPrimeAddr() string {
	return s.primeAddr
}

// GetCluster returns the lights cluster (for testing)
func (s *Server) GetCluster() *lights.Cluster {
	return s.cluster
}

// GetMessageQueueStats returns current message queue statistics (for testing)
func (s *Server) GetMessageQueueStats() QueueStats {
	if s.msgQueue == nil {
		return QueueStats{}
	}
	return s.msgQueue.GetStats()
}

// PushToPrime pushes buffered logs to prime if configured (for testing)
func (s *Server) PushToPrime() error {
	if s.msgQueue == nil {
		return nil
	}
	return s.msgQueue.PushToPrime()
}

// Start runs the headless server with MCP traffic logging
func (s *Server) Start(logPath string) error {
	// Open log file if specified
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}
		s.logFile = f
		slog.Info("headless mode logging to file", "path", logPath)
	} else {
		slog.Info("headless mode logging to stdout")
	}

	// Start MCP server if enabled
	if s.cfg.MCPServer.Enabled {
		s.mcpServer = mcpserver.NewServer(s.cfg.MCPServer, s.cluster)

		// Provide topology information to MCP server
		s.mcpServer.SetTopologyProvider(s.GetTopologyInfo)

		// Accept pushed logs from prime-plus isotopes
		s.mcpServer.SetPushLogsHandler(s.handleIncomingPushLogs)

		// Install traffic logging middleware before starting
		s.mcpServer.SetMiddleware(s.trafficLoggingMiddleware())

		if err := s.mcpServer.Start(s.ctx); err != nil {
			return fmt.Errorf("failed to start MCP server: %w", err)
		}
		slog.Info("MCP server started", "addr", s.cfg.MCPServer.Addr)
	}

	// Start smoke-alarm watcher if endpoints are configured
	if len(s.cfg.SmokeAlarm) > 0 {
		s.smokeWatcher = smokelink.NewWatcher(s.cfg.SmokeAlarm)
		updates := make(chan smokelink.LightUpdate, 32)
		s.smokeWatcher.Start(s.ctx, updates)
		go s.drainWatcherUpdates(updates)
		slog.Info("smoke-alarm watcher started", "endpoints", len(s.cfg.SmokeAlarm))
	}

	return nil
}

// drainWatcherUpdates consumes LightUpdate events from the smokelink watcher,
// applies them to the cluster, and writes them to the JSONL log.
func (s *Server) drainWatcherUpdates(ch <-chan smokelink.LightUpdate) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case upd, ok := <-ch:
			if !ok {
				return
			}
			// Apply to cluster
			lightName := "smoke:" + upd.SourceName + "/" + upd.TargetID
			l := s.cluster.GetByName(lightName)
			var oldStatus string
			if l != nil {
				oldStatus = string(l.GetStatus())
				l.SetStatus(upd.Status)
				l.SetDetails(upd.Details)
			} else {
				l = lights.New(lightName, "smoke-alarm")
				l.Source = "smoke-alarm"
				l.SetStatus(upd.Status)
				l.SetDetails(upd.Details)
				l.SourceMeta = map[string]string{
					"instance": upd.SourceName,
					"target":   upd.TargetID,
				}
				s.cluster.Add(l)
				oldStatus = "dark"
			}

			_ = s.LogTraffic(MCPTrafficLog{
				Timestamp: time.Now().UTC(),
				Type:      "light-update",
				TargetID:  upd.TargetID,
				Endpoint:  upd.SourceName,
				OldStatus: oldStatus,
				NewStatus: string(upd.Status),
				Via:       upd.Source,
			})
		}
	}
}

// GetTopologyInfo returns current topology information for MCP queries
func (s *Server) GetTopologyInfo() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	peers := []interface{}{}
	if s.msgQueue != nil {
		// If we have a message queue, we're prime-plus with a known prime
		if s.primeAddr != "" {
			peers = append(peers, map[string]interface{}{
				"name":     "prime",
				"role":     "prime",
				"endpoint": s.primeAddr,
				"status":   "active",
			})
		}
	}

	return map[string]interface{}{
		"local_name":  "adhd",
		"local_role":  string(s.role),
		"prime_addr":  s.primeAddr,
		"peers":       peers,
		"timestamp":   time.Now().UTC(),
	}
}

// trafficLoggingMiddleware returns an HTTP middleware that logs all MCP requests and responses as JSONL
func (s *Server) trafficLoggingMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Capture request body
			var method string
			var reqBody []byte
			if r.Method == http.MethodPost {
				reqBody, _ = io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(reqBody))

				// Extract method from JSON-RPC body
				var rpc struct {
					Method string `json:"method"`
				}
				_ = json.Unmarshal(reqBody, &rpc)
				method = rpc.Method
			}

			// Log request
			_ = s.LogTraffic(MCPTrafficLog{
				Timestamp: time.Now().UTC(),
				Type:      "request",
				Method:    method,
			})

			// Capture response
			rw := &responseWriter{ResponseWriter: w, code: http.StatusOK}
			next.ServeHTTP(rw, r)

			// Extract method from response if not in request
			var respMethod string
			if method != "" {
				respMethod = method
			} else {
				var rpc struct {
					Method string `json:"method"`
				}
				_ = json.Unmarshal(rw.body.Bytes(), &rpc)
				respMethod = rpc.Method
			}

			// Log response
			_ = s.LogTraffic(MCPTrafficLog{
				Timestamp: time.Now().UTC(),
				Type:      "response",
				Method:    respMethod,
			})
		})
	}
}

// responseWriter captures the response body and status code
type responseWriter struct {
	http.ResponseWriter
	code int
	body bytes.Buffer
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.code = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	rw.body.Write(b)
	return rw.ResponseWriter.Write(b)
}

// LogTraffic writes an MCP traffic entry to the log as JSONL and queues for push to prime if configured
func (s *Server) LogTraffic(entry MCPTrafficLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	// Write to stdout
	fmt.Println(string(data))

	// Write to file if configured
	if s.logFile != nil {
		if _, err := s.logFile.WriteString(string(data) + "\n"); err != nil {
			return err
		}
	}

	// Enqueue for buffering if prime-plus with message queue configured
	if s.msgQueue != nil {
		s.msgQueue.Enqueue(entry)
	}

	return nil
}

// handleIncomingPushLogs accepts log entries pushed by prime-plus isotopes and writes them to the log
func (s *Server) handleIncomingPushLogs(logs []map[string]interface{}) error {
	for _, raw := range logs {
		data, err := json.Marshal(raw)
		if err != nil {
			continue
		}

		// Write directly — don't re-enqueue to avoid prime-plus loops
		s.mu.Lock()
		fmt.Println(string(data))
		if s.logFile != nil {
			_, _ = s.logFile.WriteString(string(data) + "\n")
		}
		s.mu.Unlock()
	}
	slog.Info("received pushed logs from isotope", "count", len(logs))
	return nil
}

// RegisterAsIsotope registers this ADHD instance with smoke-alarm via MCP with its topology role
func (s *Server) RegisterAsIsotope(smokeAlarmURL string) error {
	// Determine role for registration
	role := s.role
	if role == RoleStandalone {
		// If no explicit role, infer from configuration
		if s.isPrimePlus {
			role = RolePrimePlus
		} else {
			role = RolePrime // By default, headless is prime unless configured as prime-plus
		}
	}

	// Use the new registration with role
	return RegisterIsotopeWithRole(smokeAlarmURL, role, s.cfg.MCPServer.Addr)
}

// AutoDiscoverPrime queries smoke-alarm to find the prime instance and auto-configure
// Returns true if prime was discovered and configured
func (s *Server) AutoDiscoverPrime(smokeAlarmURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	primeAddr, err := AutoDiscoverPrime(ctx, smokeAlarmURL)
	if err != nil {
		slog.Warn("failed to auto-discover prime", "error", err)
		return false
	}

	// Configure message queue to push to discovered prime
	s.SetPrimeAddr(primeAddr)
	slog.Info("auto-discovered prime instance", "endpoint", primeAddr)
	return true
}

// Shutdown cleanly stops the headless server
func (s *Server) Shutdown() error {
	s.cancel()

	if s.mcpServer != nil {
		_ = s.mcpServer.Shutdown(s.ctx)
	}

	// Flush and close message queue if configured
	if s.msgQueue != nil {
		if err := s.msgQueue.Close(); err != nil {
			slog.Warn("error closing message queue", "error", err)
		}
	}

	if s.logFile != nil {
		return s.logFile.Close()
	}

	return nil
}
