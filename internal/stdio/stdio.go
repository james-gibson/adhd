// Package stdio is adhd's MCP stdio transport (adhd#41).
//
// A stdio MCP server is a child process speaking newline-delimited JSON-RPC.
// adhd's HTTP proxy cannot reach one, and the forwarders minted by
// adhd.cluster.join could not either, so a stdio-only server (Qdrant's, an ACP
// agent, a local tool server) had to stay a direct client context. This package
// is the missing half: a persistent session per named target, a single
// initialize, and JSON-RPC id mapping over one stdout stream.
//
// It is deliberately not reachable from a request. A target is a local config
// entry, addressed by name; no request field may carry a command. `adhd.proxy`
// taking a `stdio:<command>` target would turn an unauthenticated surface into
// arbitrary command execution — a strict worsening of the SSRF it already is.
// The command lives in the operator's file, and only there.
//
// Scope is tools: initialize, tools/list, tools/call. A server→client request
// (sampling/createMessage, roots/list, elicitation/create) has nowhere to go —
// adhd's inbound transport is request/response — so it is answered with a
// method-not-found rather than dropped silently, and the limitation is
// published through List rather than guessed at by the caller.
package stdio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrUnknownTarget is returned when a caller names a target that is not
// configured. It is the only way to reach a command, so it is also the whole
// authorization surface: an unconfigured name never reaches exec.
var ErrUnknownTarget = errors.New("unknown stdio target")

// Target is one configured MCP stdio server.
type Target struct {
	Name         string
	Command      string
	Args         []string
	Env          map[string]string
	InheritEnv   []string
	Dir          string
	Timeout      time.Duration
	AllowedTools []string
}

// maxFrame bounds one line of the child's stdout. A server that answers with
// more than this is either broken or trying to exhaust memory; either way the
// session is not usable, and the read is refused rather than buffered.
const maxFrame = 8 << 20

// restartCooldown keeps a crashed child from being respawned on every call. A
// crash is a monitored failure, not a retry loop.
const restartCooldown = 5 * time.Second

// Manager owns one lazily-started session per target.
type Manager struct {
	targets  map[string]Target
	ctx      context.Context
	stop     chan struct{}
	stopOnce sync.Once
	startMu  sync.Mutex

	mu       sync.Mutex
	sessions map[string]*session
	failures map[string]failure
	observer func(target string, tools []Tool)
}

// Tool is one tool a child published. The input schema travels with it so a
// caller can see how to call the namespaced entry: a name without a schema is
// a tool a client cannot fill in.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type failure struct {
	at     time.Time
	reason string
}

// NewManager returns a manager for targets. The context bounds the children:
// when it is cancelled every session is torn down.
func NewManager(ctx context.Context, targets []Target) *Manager {
	m := &Manager{
		targets:  make(map[string]Target, len(targets)),
		ctx:      ctx,
		stop:     make(chan struct{}),
		sessions: make(map[string]*session),
		failures: make(map[string]failure),
	}
	for _, target := range targets {
		m.targets[target.Name] = target
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = m.Close()
		case <-m.stop:
		}
	}()
	return m
}

// SetToolObserver installs a callback invoked with a target's tool names after
// every tools/list it answers. adhd uses it to register the child's tools as
// namespaced surface entries, so registration follows the server rather than a
// startup enumeration that would spawn every configured process.
func (m *Manager) SetToolObserver(fn func(target string, tools []Tool)) {
	m.mu.Lock()
	m.observer = fn
	m.mu.Unlock()
}

func (m *Manager) notifyObserver(target string, tools []Tool) {
	m.mu.Lock()
	observer := m.observer
	m.mu.Unlock()
	if observer != nil {
		observer(target, tools)
	}
}

// Names returns the configured target names, sorted.
func (m *Manager) Names() []string {
	names := make([]string, 0, len(m.targets))
	for name := range m.targets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Target returns the configured target.
func (m *Manager) Target(name string) (Target, bool) {
	target, ok := m.targets[name]
	return target, ok
}

// Call forwards one JSON-RPC method to a target, starting its session if
// needed. initialize and notifications/initialized are reserved: the manager
// owns the handshake, and a caller that re-initialized the child would reset
// the session under every other caller.
func (m *Manager) Call(ctx context.Context, name, method string, params interface{}) (interface{}, error) {
	target, ok := m.targets[name]
	if !ok {
		return nil, ErrUnknownTarget
	}
	switch method {
	case "initialize", "notifications/initialized":
		return nil, fmt.Errorf("%s is owned by the stdio bridge and is not forwardable", method)
	case "tools/call":
		if tool := toolName(params); tool != "" && !allowed(target, tool) {
			return nil, fmt.Errorf("tool %q is not allowed on stdio target %q", tool, name)
		}
	}

	sess, err := m.session(target)
	if err != nil {
		return nil, err
	}

	// Every forward is bounded. A child that never answers must not hold the
	// caller's request open until the client gives up.
	callCtx := ctx
	if target.Timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, target.Timeout)
		defer cancel()
	}

	result, err := sess.call(callCtx, method, params)
	if err == nil && method == "tools/list" {
		sess.cacheTools(result)
		m.notifyObserver(target.Name, sess.toolSet())
	}
	return result, err
}

// State is one target's runtime state, for adhd.stdio.list.
type State struct {
	Name    string    `json:"name"`
	Running bool      `json:"running"`
	Tools   []string  `json:"tools,omitempty"`
	LastErr string    `json:"last_error,omitempty"`
	Since   time.Time `json:"since,omitempty"`
}

// List reports every configured target and its state. A target that was never
// called is not started by listing it: enumeration must not launch processes.
func (m *Manager) List() []State {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]State, 0, len(m.targets))
	for _, name := range m.Names() {
		state := State{Name: name}
		if sess := m.sessions[name]; sess != nil {
			state.Running = sess.isRunning()
			state.Tools = sess.toolNames()
			state.Since = sess.startedAt
		}
		if f, ok := m.failures[name]; ok {
			state.LastErr = f.reason
		}
		out = append(out, state)
	}
	return out
}

// session returns a live session for target, starting one if needed and the
// cooldown has elapsed.
func (m *Manager) session(target Target) (*session, error) {
	// Start under one lock: two callers racing on a cold target must not spawn
	// two children for the same session.
	m.startMu.Lock()
	defer m.startMu.Unlock()

	m.mu.Lock()
	if sess := m.sessions[target.Name]; sess != nil && sess.isRunning() {
		m.mu.Unlock()
		return sess, nil
	}
	if f, ok := m.failures[target.Name]; ok && time.Since(f.at) < restartCooldown {
		m.mu.Unlock()
		return nil, fmt.Errorf("stdio target %q is in cooldown after a crash: %s", target.Name, f.reason)
	}
	m.mu.Unlock()

	sess := newSession(m.ctx, target)
	if err := sess.start(); err != nil {
		m.mu.Lock()
		m.failures[target.Name] = failure{at: time.Now(), reason: err.Error()}
		m.mu.Unlock()
		return nil, fmt.Errorf("start stdio target %q: %w", target.Name, err)
	}

	m.mu.Lock()
	m.sessions[target.Name] = sess
	delete(m.failures, target.Name)
	m.mu.Unlock()

	// The tools the session found are now part of adhd's surface, namespaced by
	// target. Registration happens here rather than at startup so configuring a
	// target does not spawn it.
	m.notifyObserver(target.Name, sess.toolSet())

	// Watch the child so a crash is recorded rather than discovered later by a
	// caller whose request silently went nowhere.
	go func() {
		<-sess.done
		if !sess.wasClosed() {
			reason := sess.exitReason()
			m.mu.Lock()
			m.failures[target.Name] = failure{at: time.Now(), reason: reason}
			m.mu.Unlock()
			slog.Warn("stdio target exited", "target", target.Name, "reason", reason)
		}
	}()
	return sess, nil
}

// Close tears down every session. It kills the process group, so a wrapper
// (uvx, npx) does not leave its real server behind.
func (m *Manager) Close() error {
	m.stopOnce.Do(func() { close(m.stop) })

	m.mu.Lock()
	sessions := make([]*session, 0, len(m.sessions))
	for _, sess := range m.sessions {
		sessions = append(sessions, sess)
	}
	m.sessions = make(map[string]*session)
	m.mu.Unlock()

	var firstErr error
	for _, sess := range sessions {
		if err := sess.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// session is one child process and its JSON-RPC multiplexer.
type session struct {
	target Target
	ctx    context.Context

	cmd   *exec.Cmd
	stdin io.WriteCloser

	writeMu sync.Mutex
	nextID  atomic.Int64

	mu      sync.Mutex
	pending map[string]chan rpcMessage
	closed  bool
	cause   string
	exited  string

	done      chan struct{}
	startedAt time.Time

	initResult json.RawMessage
	tools      []Tool
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

func newSession(ctx context.Context, target Target) *session {
	if target.Timeout <= 0 {
		target.Timeout = 30 * time.Second
	}
	return &session{
		target:  target,
		ctx:     ctx,
		pending: make(map[string]chan rpcMessage),
		done:    make(chan struct{}),
	}
}

// start launches the child, performs the MCP handshake once, and begins
// demultiplexing stdout.
func (s *session) start() error {
	cmd := exec.Command(s.target.Command, s.target.Args...)
	cmd.Dir = s.target.Dir
	cmd.Env = buildEnv(s.target)

	// A wrapper such as uvx or npx is the direct child and the real server is
	// its child. Setpgid gives the group an id adhd can signal, so teardown
	// reaches both.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	s.cmd = cmd
	s.stdin = stdin
	s.startedAt = time.Now()

	go s.readStdout(stdout)
	go s.readStderr(stderr)
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		if err != nil {
			s.exited = err.Error()
		} else if cmd.ProcessState != nil {
			s.exited = cmd.ProcessState.String()
		}
		s.mu.Unlock()
		close(s.done)
	}()

	initCtx, cancel := context.WithTimeout(s.ctx, s.target.Timeout)
	defer cancel()
	initParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "adhd",
			"version": "0.1.0",
		},
	}
	result, err := s.call(initCtx, "initialize", initParams)
	if err != nil {
		_ = s.close()
		return fmt.Errorf("initialize: %w", err)
	}
	if raw, err := json.Marshal(result); err == nil {
		s.initResult = raw
	}
	if err := s.notify("notifications/initialized", nil); err != nil {
		_ = s.close()
		return fmt.Errorf("notifications/initialized: %w", err)
	}

	if err := s.refreshTools(initCtx); err != nil {
		// A server that cannot list tools can still answer initialize; keep the
		// session and let the caller see the failure on tools/list.
		slog.Warn("stdio target tools/list failed", "target", s.target.Name, "error", err)
	}
	return nil
}

// refreshTools caches the child's tool names so adhd.stdio.list can publish
// them without a second round trip.
func (s *session) refreshTools(ctx context.Context) error {
	result, err := s.call(ctx, "tools/list", nil)
	if err != nil {
		return err
	}
	s.cacheTools(result)
	return nil
}

// cacheTools records the tool names a tools/list answered with, so
// adhd.stdio.list can publish them without a second round trip.
func (s *session) cacheTools(result interface{}) {
	raw, err := json.Marshal(result)
	if err != nil {
		return
	}
	var decoded struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return
	}
	s.mu.Lock()
	s.tools = decoded.Tools
	s.mu.Unlock()
}

// call sends one request and waits for its response.
func (s *session) call(ctx context.Context, method string, params interface{}) (interface{}, error) {
	id := strconv.FormatInt(s.nextID.Add(1), 10)

	ch := make(chan rpcMessage, 1)
	s.mu.Lock()
	if s.closed {
		reason := s.cause
		s.mu.Unlock()
		return nil, fmt.Errorf("stdio session closed: %s", reason)
	}
	s.pending[id] = ch
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	// The id is a string so a child that echoes numbers cannot make the
	// demultiplexer ambiguous between 1 and "1".
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := s.write(line); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("stdio session closed: %s", s.exitReason())
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		var result interface{}
		if err := json.Unmarshal(msg.Result, &result); err != nil {
			return nil, err
		}
		return result, nil
	}
}

func (s *session) notify(method string, params interface{}) error {
	line, err := json.Marshal(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	return s.write(line)
}

func (s *session) write(line []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.stdin.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// readStdout demultiplexes newline-delimited JSON-RPC. A line that is not JSON
// is logged and skipped: servers do print banners, and one banner must not take
// the session down.
//
// The scanner carries the frame bound, so an oversized line fails at the limit
// rather than after it has been buffered whole.
func (s *session) readStdout(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), maxFrame)
	for scanner.Scan() {
		s.handleLine(scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		s.fail("read stdout: " + err.Error())
		return
	}
}

func (s *session) handleLine(line []byte) {
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		slog.Debug("stdio target wrote a non-JSON line", "target", s.target.Name, "line", strings.TrimSpace(string(line)))
		return
	}
	if msg.Method != "" {
		s.handleServerMessage(msg)
		return
	}
	id := normalizeID(msg.ID)
	s.mu.Lock()
	ch, ok := s.pending[id]
	s.mu.Unlock()
	if !ok {
		slog.Debug("stdio response for an unknown id", "target", s.target.Name, "id", id)
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

// handleServerMessage answers server→client traffic. A request gets an explicit
// error — the tool-only bridge has nowhere to route sampling or roots — so the
// child is not left hanging and the limitation is a visible answer rather than
// a stall. A notification is logged and dropped.
func (s *session) handleServerMessage(msg rpcMessage) {
	if len(msg.ID) == 0 || string(msg.ID) == "null" {
		slog.Debug("stdio target notification", "target", s.target.Name, "method", msg.Method)
		return
	}
	slog.Warn("stdio target made a server-to-client request the bridge cannot serve",
		"target", s.target.Name, "method", msg.Method)
	reply := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(msg.ID),
		"error": map[string]interface{}{
			"code":    -32601,
			"message": "adhd's stdio bridge is tools-only: " + msg.Method + " is not supported",
		},
	}
	line, err := json.Marshal(reply)
	if err != nil {
		return
	}
	if err := s.write(line); err != nil {
		slog.Warn("answer stdio server-to-client request", "target", s.target.Name, "error", err)
	}
}

func (s *session) readStderr(r io.Reader) {
	reader := bufio.NewReaderSize(r, 16<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			slog.Debug("stdio target stderr", "target", s.target.Name, "line", strings.TrimSpace(string(line)))
		}
		if err != nil {
			return
		}
	}
}

// fail records why the session died and wakes every waiting caller.
func (s *session) fail(reason string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.cause = reason
	pending := make([]chan rpcMessage, 0, len(s.pending))
	for _, ch := range s.pending {
		pending = append(pending, ch)
	}
	s.pending = make(map[string]chan rpcMessage)
	s.mu.Unlock()

	for _, ch := range pending {
		close(ch)
	}
	s.kill()
}

func (s *session) isRunning() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *session) wasClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *session) exitReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cause != "" {
		return s.cause
	}
	if s.exited != "" {
		return s.exited
	}
	return "exited"
}

// normalizeID flattens a JSON-RPC id to the key the pending map uses. The
// bridge writes ids as strings; a child that echoes a number must still match,
// so both forms reduce to the same token.
func normalizeID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	return strings.TrimSpace(string(raw))
}

func (s *session) toolNames() []string {
	tools := s.toolSet()
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}

func (s *session) toolSet() []Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tool, len(s.tools))
	copy(out, s.tools)
	return out
}

// close stops the child by process group: SIGTERM, a bounded wait, then
// SIGKILL. Killing only the direct child orphans the real server when the
// command is a wrapper, which is exactly the defect the stdio prober had
// (smoke-alarm#44).
func (s *session) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	pending := make([]chan rpcMessage, 0, len(s.pending))
	for _, ch := range s.pending {
		pending = append(pending, ch)
	}
	s.pending = make(map[string]chan rpcMessage)
	s.mu.Unlock()

	for _, ch := range pending {
		close(ch)
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if !s.kill() {
		return fmt.Errorf("stdio target %q did not exit after SIGKILL", s.target.Name)
	}
	return nil
}

// kill terminates the process group and reports whether the child is gone.
func (s *session) kill() bool {
	if s.cmd == nil || s.cmd.Process == nil {
		return true
	}
	pgid := s.cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	select {
	case <-s.done:
		return true
	case <-time.After(2 * time.Second):
	}

	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	select {
	case <-s.done:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

// buildEnv is explicit, never os.Environ(). A child that inherits the parent
// environment receives every API key adhd was opened with; InheritEnv names the
// variables the operator decided to pass.
func buildEnv(target Target) []string {
	env := make([]string, 0, len(target.InheritEnv)+len(target.Env))
	for _, name := range target.InheritEnv {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	for name, value := range target.Env {
		env = append(env, name+"="+value)
	}
	sort.Strings(env)
	return env
}

func allowed(target Target, tool string) bool {
	if len(target.AllowedTools) == 0 {
		return true
	}
	for _, name := range target.AllowedTools {
		if name == tool {
			return true
		}
	}
	return false
}

func toolName(params interface{}) string {
	m, ok := params.(map[string]interface{})
	if !ok {
		return ""
	}
	name, _ := m["name"].(string)
	return name
}
