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
	"strings"
	"sync"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpserver"
	"github.com/james-gibson/adhd/internal/smokelink"
	"github.com/james-gibson/isotope"
)

// Server runs ADHD in headless mode, logging all MCP traffic as JSONL
type Server struct {
	cfg          *config.Config
	cluster      *lights.Cluster
	mcpServer    *mcpserver.Server
	smokeWatcher *smokelink.Watcher
	watchUpdates chan smokelink.LightUpdate // receives watcher events; set in Start()
	logFile      *os.File
	msgQueue     *MessageQueue
	isPrimePlus  bool
	role         IsotopeRole
	primeAddr    string
	trustLevel   int
	zeroconfSrv  isotope.Server
	identity     isotope.InstanceIdentity
	forceAdvert  bool // --force: advertise even when the MCP bind is unguarded
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
		cfg:      cfg,
		cluster:  cluster,
		ctx:      ctx,
		cancel:   cancel,
		role:     RoleStandalone, // Default role until configured
		identity: isotope.NewInstanceIdentity(),
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

		// Wire clusters discovered by --demo before any join push arrives, so a
		// headless instance started from a discovered config still exposes the
		// clusters' adhd.project.<route>.call proxy tools.
		s.mcpServer.RegisterDiscoveredClusters(s.cfg.DiscoveredClusters)

		// Provide topology information to MCP server
		s.mcpServer.SetTopologyProvider(s.GetTopologyInfo)

		// Accept pushed logs from prime-plus isotopes
		s.mcpServer.SetPushLogsHandler(s.handleIncomingPushLogs)

		// Wire rung validation identity — each instance has a unique secret
		s.mcpServer.SetInstanceIdentity(
			s.identity.Isotope,
			s.identity.ComputeReceipt,
			s.identity.VerifyReceipt,
		)

		// Install traffic logging middleware before starting
		s.mcpServer.SetMiddleware(s.trafficLoggingMiddleware())

		if err := s.mcpServer.Start(s.ctx); err != nil {
			return fmt.Errorf("failed to start MCP server: %w", err)
		}
		slog.Info("MCP server started", "addr", s.cfg.MCPServer.Addr)
	}

	// Start smoke-alarm watcher. Always create it so the cluster-join handler
	// can add endpoints at runtime even when no initial endpoints are configured.
	s.smokeWatcher = smokelink.NewWatcher(s.cfg.SmokeAlarm)
	s.watchUpdates = make(chan smokelink.LightUpdate, 32)
	s.smokeWatcher.Start(s.ctx, s.watchUpdates)
	go s.drainWatcherUpdates(s.watchUpdates)
	if len(s.cfg.SmokeAlarm) > 0 {
		slog.Info("smoke-alarm watcher started", "endpoints", len(s.cfg.SmokeAlarm))
	}

	// Wire cluster-join handler so lezz demo can push new endpoints via MCP.
	if s.mcpServer != nil {
		s.mcpServer.SetClusterJoinHandler(s.handleClusterJoin)
		s.mcpServer.SetSecurityAlertHandler(s.reportSecurityAlert)
	}

	return nil
}

// reportSecurityAlert pushes a security event to all configured smoke-alarm instances
// via their /federation/report endpoint, surfacing it as a degraded component.
func (s *Server) reportSecurityAlert(level, event string, details map[string]interface{}) {
	if len(s.cfg.SmokeAlarm) == 0 {
		return
	}

	component := map[string]interface{}{
		"name":       "security/" + event,
		"healthy":    false,
		"detail":     fmt.Sprintf("level=%s %v", level, details),
		"updated_at": time.Now().UTC(),
	}
	payload := map[string]interface{}{
		"service":    "adhd",
		"live":       true,
		"ready":      true,
		"now":        time.Now().UTC(),
		"components": []interface{}{component},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	for _, ep := range s.cfg.SmokeAlarm {
		base := strings.TrimSuffix(ep.Endpoint, "/")
		url := base + "/federation/report"
		req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			slog.Debug("security alert: federation report failed", "url", url, "error", err)
			continue
		}
		_ = resp.Body.Close()
		slog.Info("security alert reported to smoke-alarm",
			"event", event,
			"level", level,
			"smoke_alarm", url,
		)
	}
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
			// Skip heartbeat updates (IsInstance=true, empty TargetID) — they exist
			// only to keep the Bubble Tea command pipeline alive in TUI mode and
			// carry no cluster state for headless mode.
			if upd.IsInstance {
				continue
			}
			// Skip remote-feature batches — handled by TUI only.
			if len(upd.RemoteFeatures) > 0 {
				continue
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
// handleClusterJoin wires a newly-joined lezz demo cluster's smoke-alarm
// endpoints into the running watcher so their health updates flow into the
// headless cluster immediately — no wait for the next poll cycle.
func (s *Server) handleClusterJoin(name, alarmA, alarmB string) error {
	prefix := name + "/"
	if alarmA != "" {
		s.smokeWatcher.WatchEndpoint(s.ctx, config.SmokeAlarmEndpoint{
			Name:     prefix + "alarm-a",
			Endpoint: alarmA,
			Interval: 10 * time.Second,
		}, s.watchUpdates)
	}
	if alarmB != "" {
		s.smokeWatcher.WatchEndpoint(s.ctx, config.SmokeAlarmEndpoint{
			Name:     prefix + "alarm-b",
			Endpoint: alarmB,
			Interval: 10 * time.Second,
		}, s.watchUpdates)
	}
	return nil
}

func (s *Server) GetTopologyInfo() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	peers := []interface{}{}
	if s.msgQueue != nil {
		// If we have a message queue, we're prime-plus with a known prime
		if s.primeAddr != "" {
			peers = append(peers, map[string]interface{}{
				"name":       "prime",
				"role":       "prime",
				"endpoint":   s.primeAddr,
				"status":     "active",
				"trust_rung": s.trustLevel,
			})
		}
	}

	return map[string]interface{}{
		"local_name": "adhd",
		"local_role": string(s.role),
		"prime_addr": s.primeAddr,
		"peers":      peers,
		"timestamp":  time.Now().UTC(),
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

// Instance label shape. DNS-SD instance labels are limited to 63 bytes, and the
// isotope identity is a 43-character base64url string (A–Z a–z 0–9 - _), all of
// which is legal in a label. Eight characters is enough to be unique across a
// LAN (2^-48) while staying readable in a service browser.
const (
	instanceLabelPrefix = "adhd-"
	instanceLabelIDLen  = 8
)

// advertiseMDNS is the mDNS registration seam. Tests replace it to assert which
// instance label is published without putting a record on the network.
var advertiseMDNS = isotope.Advertise

// instanceLabel returns the LAN-unique name this process registers and
// advertises.
//
// A constant instance name ("adhd") is not an identity. Two headless instances
// — one per lezz demo cluster, for instance — publish the same DNS-SD name, so
// consumers keyed by name (`resolved[service.name]` in lezz.sea) collapse them
// into one entry, and a deregistration from either removes both (adhd#48). The
// isotope identity is already generated per process and already public — it
// names the rung receipts — so the label derives from it: stable for the process
// lifetime, and unique across the LAN rather than only across the host. A
// port-derived label (`adhd-9190`) would collide again the moment two hosts use
// the same port.
//
// smoke-alarm keys its isotope registry by name, so this label is used for the
// registration as well as the advertisement: two instances must be two entries,
// not one entry overwritten by whichever registered last.
func (s *Server) instanceLabel() string {
	if id := strings.TrimSpace(s.identity.Isotope); id != "" {
		if len(id) > instanceLabelIDLen {
			id = id[:instanceLabelIDLen]
		}
		return instanceLabelPrefix + id
	}

	// Defensive only: New always sets an identity. A pid is unique on this host
	// but not across a LAN; it is still better than a constant that means "any
	// adhd", which is the bug this function exists to prevent.
	return fmt.Sprintf("%s%d", instanceLabelPrefix, os.Getpid())
}

// SetForceAdvertise bypasses the advertise guard for the deliberate
// LAN-discovery case (e.g. `lezz demo`). It is an explicit operator override
// and never the default: an unguarded non-loopback MCP endpoint that is
// advertised is an alarm offense.
func (s *Server) SetForceAdvertise(force bool) {
	s.forceAdvert = force
}

// RegisterAsIsotope registers this ADHD instance with smoke-alarm and stores the assigned trust rung.
func (s *Server) RegisterAsIsotope(smokeAlarmURL string) error {
	// Fail closed before emitting anything: refuse to name an unauthenticated
	// MCP endpoint (to smoke-alarm or via mDNS) unless an auth gateway answers
	// on it or the operator passed --force.
	if !s.forceAdvert {
		if err := GuardAdvertisedEndpoint(s.ctx, s.cfg.MCPServer.Addr); err != nil {
			return err
		}
	}

	// Determine role for registration
	role := s.role
	if role == RoleStandalone {
		if s.isPrimePlus {
			role = RolePrimePlus
		} else {
			role = RolePrime
		}
	}

	label := s.instanceLabel()

	rung, err := RegisterIsotopeWithRole(context.Background(), smokeAlarmURL, role, s.cfg.MCPServer.Addr, label)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.trustLevel = rung
	s.mu.Unlock()

	// Advertise via mDNS so other nodes on the LAN can discover this isotope.
	// The instance label is this instance, not the product name: a constant
	// label makes two headless instances indistinguishable to every browser
	// that keys by name (adhd#48).
	if zSrv, mdnsErr := advertiseMDNS(label, "_adhd-isotope._tcp", s.cfg.MCPServer.Addr, string(role), rung); mdnsErr != nil {
		slog.Warn("isotope mDNS advertisement failed", "error", mdnsErr)
	} else {
		s.mu.Lock()
		s.zeroconfSrv = zSrv
		s.mu.Unlock()
	}

	return nil
}

// TrustLevel returns the 42i trust rung assigned by smoke-alarm during registration.
func (s *Server) TrustLevel() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trustLevel
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

	s.mu.Lock()
	zSrv := s.zeroconfSrv
	s.mu.Unlock()
	if zSrv != nil {
		zSrv.Shutdown()
	}

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
