package mcpserver

import (
	"net/http"

	"github.com/james-gibson/adhd/internal/entitle"
)

// Capability rungs. These are the shield gate's tiers, not the isotope
// certification rung (0..4) that smoke-alarm assigns: one says what a caller
// may drive, the other says how much an instance has proved. They share the
// word "rung" because kb's gate already put it on the wire, and matching that
// contract is worth more than a second vocabulary.
const (
	capRead   = 0 // status, lights, isotope, rung negotiation
	capWrite  = 3 // cluster membership
	capExec   = 4 // proxy, paths, repo alerts, stdio enumeration
	capDanger = 6 // shell execution and anything unknown
	// Unknown methods are capDanger: a gate that guesses reads for an
	// unrecognised method guesses open, and the method namespace grows.
)

// methodTiers is the per-method entitlement table.
var methodTiers = map[string]int{
	"initialize":                    capRead,
	"notifications/initialized":     capRead,
	"ping":                          capRead,
	"tools/list":                    capRead,
	"tools/call":                    capRead, // the tool's own tier decides
	"adhd.status":                   capRead,
	"adhd.lights.list":              capRead,
	"adhd.lights.get":               capRead,
	"adhd.isotope.status":           capRead,
	"adhd.isotope.peers":            capRead,
	"adhd.isotope.instance":         capRead,
	"adhd.rung.respond":             capRead,
	"adhd.rung.verify":              capRead,
	"adhd.rung.challenge":           capExec,
	"adhd.cluster.join":             capWrite,
	"adhd.cluster.leave":            capWrite,
	"adhd.proxy":                    capExec,
	"adhd.path.negotiate":           capExec,
	"adhd.path.verify":              capExec,
	"adhd.path.list":                capExec,
	"adhd.path.call":                capExec,
	"adhd.pr":                       capExec,
	"adhd.dependabot.alerts":        capExec,
	"adhd.stdio.list":               capExec,
	"smoke-alarm.isotope.push-logs": capWrite,
	"adhd.hurl.run":                 capDanger,
	"adhd.gh.run":                   capDanger,
}

// toolTiers is the entitlement for a tool named inside tools/call. Dynamic
// tools carry their own tier; this table covers the static surface.
var toolTiers = map[string]int{
	"adhd.status":            capRead,
	"adhd.lights.list":       capRead,
	"adhd.lights.get":        capRead,
	"adhd.isotope.status":    capRead,
	"adhd.isotope.peers":     capRead,
	"adhd.isotope.instance":  capRead,
	"adhd.rung.respond":      capRead,
	"adhd.rung.verify":       capRead,
	"adhd.rung.challenge":    capExec,
	"adhd.cluster.join":      capWrite,
	"adhd.cluster.leave":     capWrite,
	"adhd.proxy":             capExec,
	"adhd.path.negotiate":    capExec,
	"adhd.path.verify":       capExec,
	"adhd.path.list":         capExec,
	"adhd.path.call":         capExec,
	"adhd.pr":                capExec,
	"adhd.dependabot.alerts": capExec,
	"adhd.stdio.list":        capExec,
	"adhd.hurl.run":          capDanger,
	"adhd.gh.run":            capDanger,
}

// methodTier returns the tier a method requires. It mirrors kb's shield gate:
// a project proxy call and a stdio bridge are exec — the caller cannot choose
// the command, only a configured target — and an unknown method is capDanger.
func methodTier(method string) int {
	if tier, ok := methodTiers[method]; ok {
		return tier
	}
	switch {
	case hasPrefixSuffix(method, "adhd.project.", ".call"):
		return capExec
	case hasPrefixSuffix(method, "adhd.stdio.", ".call"):
		return capExec
	}
	return capDanger
}

// toolTier returns the tier a tool requires.
func toolTier(name string) int {
	if tier, ok := toolTiers[name]; ok {
		return tier
	}
	switch {
	case hasPrefixSuffix(name, "adhd.project.", ".call"):
		return capExec
	case hasPrefixSuffix(name, "adhd.stdio.", ".call"):
		return capExec
	}
	return capDanger
}

func hasPrefixSuffix(s, prefix, suffix string) bool {
	return len(s) >= len(prefix)+len(suffix) &&
		s[:len(prefix)] == prefix && s[len(s)-len(suffix):] == suffix
}

// SetGate installs the inbound entitlement gate. A nil gate leaves the surface
// as it was: open to any caller, which SECURITY.md and adhd#27 describe.
func (s *Server) SetGate(gate *entitle.Gate) {
	s.mu.Lock()
	s.gate = gate
	s.mu.Unlock()
}

// gateDecision is what authorize produced for one request.
type gateDecision struct {
	// params is the request's params with the signature removed, ready to
	// dispatch.
	params interface{}
	// rung is the effective capability; 0 when the gate is off.
	rung int
	// refused means the caller may not dispatch this request and the response
	// is already shaped in result/err.
	refused bool
	result  interface{}
	err     *jsonrpcError
}

// authorize verifies the caller and reports whether the method is within its
// capability.
//
// A refusal is deliberately shaped as "method not found" rather than 401/403:
// a caller that learns "this exists but you may not have it" learns the map the
// gate is meant to withhold (kb/scripts/cluster/shield_gate.py, same rule).
func (s *Server) authorize(r *http.Request, method string, params interface{}) gateDecision {
	s.mu.RLock()
	gate := s.gate
	s.mu.RUnlock()
	if gate == nil {
		// No gate: every caller has the whole surface, which is what a build
		// without a keyring has always done.
		return gateDecision{params: params, rung: capDanger}
	}

	verdict := gate.Verify(method, params, r.Header.Get("Authorization"))
	caller, _ := r.Context().Value(callerIPKey{}).(string)

	if !verdict.Entitled {
		tool := toolNameFromParams(params)
		if method != "tools/call" {
			// A direct method call names no tool; the method is the target.
			tool = method
		}
		if s.refusedDrive(method, tool) {
			s.fileUnentitled(caller, method, tool, 0, methodTier(method), verdict.Identity, verdict.Reason)
		}
		result, err := s.facadeResponse(method, params)
		return gateDecision{refused: true, result: result, err: err}
	}

	// tools/call is checked against the named tool, not against the word
	// "adhd.proxy". Anything else uses the method table.
	name := toolNameFromParams(verdict.Params)
	required := methodTier(method)
	if method == "tools/call" {
		required = s.requiredTier(name)
	} else if tier, ok := s.dynamicTier(method); ok {
		required = tier
	}
	if required > verdict.Rung {
		s.fileUnentitled(caller, method, name, verdict.Rung, required, verdict.Identity, "insufficient capability")
		result, err := s.facadeResponse(method, verdict.Params)
		return gateDecision{refused: true, rung: verdict.Rung, result: result, err: err}
	}
	return gateDecision{params: verdict.Params, rung: verdict.Rung}
}

// fileUnentitled raises the security light and files the event. A refusal that
// does not file is a silent gate: the caller is refused and the operator never
// learns the surface was driven.
func (s *Server) fileUnentitled(caller, method, tool string, rung, required int, identity, reason string) {
	details := map[string]interface{}{
		"caller_ip": caller,
		"method":    method,
		"rung":      rung,
		"required":  required,
		"identity":  identity,
		"reason":    reason,
	}
	if tool != "" {
		details["tool"] = tool
	}
	s.fireSecurity("warn", "unentitled_tool_call", details)
}

// facadeResponse is the empty adhd an unentitled caller sees. It is
// indistinguishable from an instance with no tools, no peers and no lights, so
// probing reveals neither the gate nor the real surface.
func (s *Server) facadeResponse(method string, params interface{}) (interface{}, *jsonrpcError) {
	switch method {
	case "initialize":
		// A client must be able to connect to be told there is nothing here.
		return s.handleInitialize(), nil
	case "ping":
		return map[string]interface{}{"pong": true}, nil
	case "notifications/initialized":
		return map[string]interface{}{}, nil
	case "tools/list":
		return map[string]interface{}{"tools": []interface{}{}}, nil
	case "tools/call":
		return nil, &jsonrpcError{Code: -32601, Message: "unknown tool"}
	case "adhd.status":
		return map[string]interface{}{
			"cluster_peers": []interface{}{},
			"dynamic_tools": 0,
			"instance":      "adhd",
			"isotope":       "",
			"lights": map[string]int{
				"total":  0,
				"green":  0,
				"red":    0,
				"yellow": 0,
				"dark":   0,
			},
			"paths": 0,
		}, nil
	case "adhd.lights.list":
		return map[string]interface{}{"lights": []interface{}{}}, nil
	case "adhd.lights.get":
		return map[string]interface{}{"light": nil}, nil
	case "adhd.isotope.peers":
		return map[string]interface{}{"peers": []interface{}{}}, nil
	case "adhd.isotope.status":
		return map[string]interface{}{"isotope": "", "rung": 0, "peers": []interface{}{}}, nil
	case "adhd.isotope.instance":
		return map[string]interface{}{"instance": ""}, nil
	case "adhd.rung.respond":
		return s.handleRungRespond(params)
	case "adhd.rung.verify":
		return s.handleRungVerify(params)
	}
	return nil, &jsonrpcError{Code: -32601, Message: "Method not found: " + method}
}

// requiredTier is the tier a tool named inside tools/call requires. A dynamic
// tool carries its own; a static one comes from the table.
func (s *Server) requiredTier(name string) int {
	if tier, ok := s.dynamicTier(name); ok {
		return tier
	}
	return toolTier(name)
}

// dynamicTier returns a runtime-registered tool's tier. Registration sets it
// explicitly; a tool that somehow has none is capDanger, because the zero value
// of a missing capability must not read as "read".
func (s *Server) dynamicTier(name string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, tool := range s.dynamicTools {
		if tool.name == name {
			if tool.tier == 0 {
				return capDanger, true
			}
			return tool.tier, true
		}
	}
	return 0, false
}

// refusedDrive reports whether a refused request is an attempt to *drive*
// something rather than to read what is already public.
//
// The distinction matters because the alarm is a shared resource: an unentitled
// caller asking for adhd.status or challenging the isotope is a monitor doing
// its job, and filing every probe would bury the signal under noise. Driving an
// exec-tier tool — adhd.gh.run, adhd.proxy, a shell side effect — is the event
// that must never be silent.
func (s *Server) refusedDrive(method, tool string) bool {
	if method == "tools/call" {
		if tool == "" {
			return false
		}
		if tier, ok := s.dynamicTier(tool); ok {
			return tier > capRead
		}
		if tier, ok := toolTiers[tool]; ok {
			return tier > capRead
		}
		// A name adhd does not serve is a guess, not a drive.
		return false
	}
	if !s.isRealMethod(method) {
		return false
	}
	return methodTier(method) > capRead
}

// isRealMethod reports whether method is one adhd actually serves (or a
// registered dynamic tool).
func (s *Server) isRealMethod(method string) bool {
	if _, ok := methodTiers[method]; ok {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, tool := range s.dynamicTools {
		if tool.name == method {
			return true
		}
	}
	return false
}

func toolNameFromParams(params interface{}) string {
	m, ok := params.(map[string]interface{})
	if !ok {
		return ""
	}
	name, _ := m["name"].(string)
	return name
}
