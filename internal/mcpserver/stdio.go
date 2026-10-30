package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/james-gibson/adhd/internal/stdio"
)

// This file is the MCP face of the stdio transport (adhd#41).
//
// A stdio server is reached by *name* from local config. The shape is
// deliberately narrow:
//
//	adhd.stdio.<target>.call   {method, params}   the bridge, tools/list and tools/call
//	adhd.stdio.<target>.<tool> {…}                each tool the child publishes
//	adhd.stdio.list                               targets, state and the namespaced tools
//
// and `adhd.project.<route>.call` resolves `mcp_url: stdio:<target>` the same
// way, so a project published by lezz works without changing its call shape.
//
// The safety constraint is structural, not a check: no request field can name a
// command. `adhd.proxy` stays URL-only, and a request that carries a command is
// refused and filed as `stdio_knock`.

// SetStdioManager installs the stdio transport. It is nil until targets are
// configured, so an instance with no stdio_targets has no stdio surface at all.
func (s *Server) SetStdioManager(manager *stdio.Manager) {
	if manager == nil {
		return
	}
	s.mu.Lock()
	s.stdio = manager
	s.mu.Unlock()

	manager.SetToolObserver(func(target string, tools []stdio.Tool) {
		s.registerStdioTools(target, tools)
	})

	// A list tool exists only when there is something to list; an empty
	// adhd.stdio.list would advertise a capability this instance does not have.
	s.registerDynamic(&dynamicTool{
		name:        "adhd.stdio.list",
		description: "List configured stdio MCP targets, whether each is running, and the tools it publishes (namespaced adhd.stdio.<target>.<tool>).",
		origin:      "stdio:list",
		tier:        capExec,
		inputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		handler: func(_ context.Context, _ interface{}) (interface{}, *jsonrpcError) {
			return s.stdioList(), nil
		},
	})

	for _, name := range manager.Names() {
		s.registerStdioBridge(name)
	}
}

// recordCollision keeps a refused name visible in adhd.stdio.list.
func (s *Server) recordCollision(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, seen := range s.stdioCollisions {
		if seen == name {
			return
		}
	}
	s.stdioCollisions = append(s.stdioCollisions, name)
}

// stdioList reports every target and its state, plus any collision that kept a
// child tool off the surface. A collision is reported rather than dropped:
// silently hiding one of two tools with the same name is how a caller ends up
// driving the wrong process.
func (s *Server) stdioList() interface{} {
	s.mu.RLock()
	manager := s.stdio
	s.mu.RUnlock()
	if manager == nil {
		return map[string]interface{}{"targets": []interface{}{}, "collisions": []string{}}
	}

	states := manager.List()
	targets := make([]map[string]interface{}, 0, len(states))
	for _, state := range states {
		entry := map[string]interface{}{
			"name":    state.Name,
			"running": state.Running,
		}
		if len(state.Tools) > 0 {
			namespaced := make([]string, 0, len(state.Tools))
			for _, tool := range state.Tools {
				namespaced = append(namespaced, stdioToolName(state.Name, tool))
			}
			entry["tools"] = namespaced
		}
		if state.LastErr != "" {
			entry["last_error"] = state.LastErr
		}
		targets = append(targets, entry)
	}

	s.mu.RLock()
	collisions := append([]string(nil), s.stdioCollisions...)
	s.mu.RUnlock()
	if collisions == nil {
		collisions = []string{}
	}
	return map[string]interface{}{
		"targets":    targets,
		"collisions": collisions,
	}
}

// registerStdioBridge registers adhd.stdio.<name>.call.
func (s *Server) registerStdioBridge(name string) {
	toolName := "adhd.stdio." + name + ".call"
	outcome := s.registerDynamic(&dynamicTool{
		name:        toolName,
		description: fmt.Sprintf("Forward a JSON-RPC call to the %s MCP server over stdio (tools/list and tools/call).", name),
		origin:      "stdio:" + name,
		tier:        capExec,
		inputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"method": map[string]interface{}{
					"type":        "string",
					"description": "MCP method to call on the stdio server (tools/list, tools/call)",
				},
				"params": map[string]interface{}{
					"type":        "object",
					"description": "Optional parameters for the method",
				},
				"tool": map[string]interface{}{
					"type":        "string",
					"description": "Shorthand for params.name when method is tools/call",
				},
				"arguments": map[string]interface{}{
					"type":        "object",
					"description": "Shorthand for params.arguments when method is tools/call",
				},
			},
		},
		handler: func(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
			method, callParams, err := bridgeCall(params)
			if err != nil {
				return nil, &jsonrpcError{Code: -32602, Message: err.Error()}
			}
			return s.callStdio(ctx, name, method, callParams)
		},
	})
	if outcome == registrationConflict {
		slog.Warn("stdio bridge name collision", "tool", toolName, "target", name)
		s.recordCollision(toolName)
	}
}

// registerStdioTools registers one namespaced tool per tool the child
// publishes. A name that is already taken by a different origin is refused and
// reported, never overwritten.
func (s *Server) registerStdioTools(target string, tools []stdio.Tool) {
	for _, tool := range tools {
		name := stdioToolName(target, tool.Name)
		childTool := tool.Name
		outcome := s.registerDynamic(&dynamicTool{
			name:        name,
			description: childDescription(tool, target),
			origin:      "stdio:" + target + ":" + childTool,
			tier:        capExec,
			inputSchema: childSchema(tool),
			handler: func(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
				return s.callStdio(ctx, target, "tools/call", map[string]interface{}{
					"name":      childTool,
					"arguments": params,
				})
			},
		})
		if outcome == registrationConflict {
			slog.Warn("stdio tool name collision", "tool", name, "target", target, "child_tool", childTool)
			s.recordCollision(name)
		}
	}
}

// childDescription names the tool and the target it belongs to.
func childDescription(tool stdio.Tool, target string) string {
	if tool.Description == "" {
		return fmt.Sprintf("%s (stdio target %s)", tool.Name, target)
	}
	return fmt.Sprintf("%s (stdio target %s)", tool.Description, target)
}

// childSchema passes the child's input schema through so a client can fill the
// call in. A missing schema becomes an empty object, not a fabricated one.
func childSchema(tool stdio.Tool) map[string]interface{} {
	if len(tool.InputSchema) > 0 {
		var schema map[string]interface{}
		if err := json.Unmarshal(tool.InputSchema, &schema); err == nil && len(schema) > 0 {
			return schema
		}
	}
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}

// registerDynamic adds or resolves a runtime tool with collision reporting.
func (s *Server) registerDynamic(tool *dynamicTool) registrationOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.dynamicTools {
		if existing.name != tool.name {
			continue
		}
		if existing.origin == tool.origin {
			return registrationUnchanged
		}
		return registrationConflict
	}
	s.dynamicTools = append(s.dynamicTools, tool)
	return registrationAdded
}

func (s *Server) callStdio(ctx context.Context, target, method string, params interface{}) (interface{}, *jsonrpcError) {
	s.mu.RLock()
	manager := s.stdio
	s.mu.RUnlock()
	if manager == nil {
		return nil, &jsonrpcError{Code: -32000, Message: "stdio transport is not configured"}
	}
	result, err := manager.Call(ctx, target, method, params)
	if err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: err.Error()}
	}
	return result, nil
}

// bridgeCall normalises the two accepted shapes: {method, params} and the
// {tool, arguments} shorthand.
func bridgeCall(params interface{}) (string, interface{}, error) {
	m, ok := params.(map[string]interface{})
	if !ok {
		return "", nil, fmt.Errorf("params must be an object")
	}
	if tool, _ := m["tool"].(string); tool != "" {
		args := m["arguments"]
		if args == nil {
			args = map[string]interface{}{}
		}
		return "tools/call", map[string]interface{}{"name": tool, "arguments": args}, nil
	}
	method, _ := m["method"].(string)
	if method == "" {
		return "", nil, fmt.Errorf("method is required")
	}
	return method, m["params"], nil
}

// stdioToolName is the namespaced name of a child tool. The target prefix is
// the namespace: two stdio servers may both publish `search` without one
// shadowing the other.
func stdioToolName(target, tool string) string {
	return "adhd.stdio." + target + "." + tool
}

// stdioTargetFromEndpoint reports the target named by an `mcp_url`/endpoint of
// the form "stdio:<name>". It is the only place a project endpoint may name a
// process, and it names one that is already configured — never a command.
func stdioTargetFromEndpoint(endpoint string) (string, bool) {
	const prefix = "stdio:"
	if !strings.HasPrefix(endpoint, prefix) {
		return "", false
	}
	name := strings.TrimSpace(strings.TrimPrefix(endpoint, prefix))
	if name == "" {
		return "", false
	}
	return name, true
}

// refuseStdioTarget is the knock. A request that carries a command is refused
// and filed, whether or not a stdio target exists: the attempt is the signal,
// and detection stays separate from authorization.
func (s *Server) refuseStdioTarget(caller string, params map[string]interface{}) {
	details := map[string]interface{}{
		"caller_ip": caller,
		"via":       "adhd.proxy",
	}
	if endpoint, ok := params["target_endpoint"].(string); ok {
		details["target_endpoint"] = endpoint
	}
	for _, key := range []string{"command", "cmd", "args", "argv"} {
		if value, present := params[key]; present {
			details[key] = value
		}
	}
	s.fireSecurity("warn", "stdio_knock", details)
}

// stdioKnock reports whether params try to name a command through the proxy.
func stdioKnock(params map[string]interface{}) bool {
	if endpoint, ok := params["target_endpoint"].(string); ok {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(endpoint)), "stdio:") {
			return true
		}
	}
	for _, key := range []string{"command", "cmd", "args", "argv"} {
		if _, present := params[key]; present {
			return true
		}
	}
	return false
}

// stdioManager is a tiny accessor used by tests and by project tools.
func (s *Server) stdioManager() *stdio.Manager {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stdio
}
