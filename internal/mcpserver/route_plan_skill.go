package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/james-gibson/adhd/internal/mirror"
	"github.com/james-gibson/adhd/internal/navtrace"
)

// RoutePlanSkillName is the visible skill that resolves a target isotope into
// one callable hop chain.
const RoutePlanSkillName = "adhd.skill.route-plan.call"

// routePlan is the cached answer for one (target, feature) pair. Planning is
// idempotent within a tick, so a second caller gets the same chain instead of
// re-running the challenge walk.
type routePlan struct {
	tick   uint64
	result map[string]interface{}
}

// RegisterRoutePlanSkill installs the skill under its well-known name.
func (s *Server) RegisterRoutePlanSkill() {
	s.RegisterRoutePlanSkillAs(RoutePlanSkillName)
}

// RegisterRoutePlanSkillAs installs the routing planner as a dynamic tool.
//
// It is deliberately visible: a planner that cannot ask "what is the chain to
// this target?" has to walk the registry itself and duplicate the discovery
// rules. The answer is one path, already sorted by trust then length, with the
// segments a caller can present to adhd.path.call unchanged.
func (s *Server) RegisterRoutePlanSkillAs(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.dynamicTools {
		if t.name == name {
			return
		}
	}
	s.dynamicTools = append(s.dynamicTools, &dynamicTool{
		name:        name,
		description: "Resolve a target isotope to one callable hop chain. Returns the segments for adhd.path.call, the hop list, the tick and the minimum rung.",
		inputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"target.isotope": map[string]interface{}{
					"type":        "string",
					"description": "The isotope to reach.",
				},
				"feature_id": map[string]interface{}{
					"type":        "string",
					"description": "The feature the route must be verified against.",
				},
				"max_hops": map[string]interface{}{
					"type":        "number",
					"description": "Discovery depth (default 4, capped at 16). Routing itself has no depth cap.",
				},
			},
			"required": []string{"target.isotope", "feature_id"},
		},
		handler: func(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
			return s.planRoute(ctx, name, params)
		},
	})
}

func (s *Server) planRoute(ctx context.Context, skillName string, params interface{}) (interface{}, *jsonrpcError) {
	m, ok := params.(map[string]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "params must be an object"}
	}
	target, _ := m["target.isotope"].(string)
	if target == "" {
		return nil, &jsonrpcError{Code: -32602, Message: "target.isotope is required"}
	}
	feature, _ := m["feature_id"].(string)
	if feature == "" {
		return nil, &jsonrpcError{Code: -32602, Message: "feature_id is required"}
	}
	maxHops := 4
	if v, ok := m["max_hops"].(float64); ok && int(v) > 0 {
		maxHops = int(v)
	}
	if maxHops > 16 {
		maxHops = 16
	}

	now, ticked := s.nowTick()
	key := target + "\x00" + feature
	if ticked {
		s.mu.Lock()
		cached, found := s.routePlans[key]
		for k, entry := range s.routePlans {
			if entry.tick <= now && now-entry.tick > PathGraceTicks {
				delete(s.routePlans, k)
			}
		}
		s.mu.Unlock()
		if found && cached.tick <= now && now-cached.tick <= PathGraceTicks {
			out := make(map[string]interface{}, len(cached.result)+1)
			for k, v := range cached.result {
				out[k] = v
			}
			out["cached"] = true
			s.mirrorPlan(skillName, target, feature, cached.tick)
			return out, nil
		}
	}

	negotiated, rpcErr := s.handlePathNegotiate(ctx, map[string]interface{}{
		"target.isotope": target,
		"feature_id":     feature,
		"max_hops":       float64(maxHops),
	})
	if rpcErr != nil {
		return nil, rpcErr
	}
	envelope, _ := negotiated.(map[string]interface{})
	paths, _ := envelope["paths"].([]interface{})
	if len(paths) == 0 {
		return nil, &jsonrpcError{
			Code:    -32000,
			Message: fmt.Sprintf("no route to %s for feature %s within %d hops", target, feature, maxHops),
		}
	}

	// handlePathNegotiate already sorts by highest minimum rung, then shortest.
	best, _ := paths[0].(map[string]interface{})
	path, decodeErr := decodePathToken(best)
	if decodeErr != nil {
		return nil, &jsonrpcError{Code: -32603, Message: decodeErr.Error()}
	}

	chain := make([]interface{}, 0, len(path.Hops))
	for i, hop := range path.Hops {
		chain = append(chain, map[string]interface{}{
			"step":     i,
			"isotope":  hop.Isotope,
			"endpoint": hop.Endpoint,
			"rung":     hop.Rung,
		})
	}

	result := map[string]interface{}{
		"target":       path.Target,
		"feature_id":   path.FeatureID,
		"tick":         path.Tick,
		"skill":        navtrace.ID(skillName),
		"min_rung":     path.MinRung,
		"hops":         len(path.Hops),
		"chain":        chain,
		"segments":     best["segments"],
		"alternatives": len(paths) - 1,
		"cached":       false,
	}
	if ticked {
		s.mu.Lock()
		s.routePlans[key] = routePlan{tick: now, result: result}
		s.mu.Unlock()
	}
	s.mirrorPlan(skillName, path.Target, feature, path.Tick)
	return result, nil
}

// mirrorPlan sends the plan to the backup as metadata only: which skill
// resolved which target for which tick, never the segments themselves.
func (s *Server) mirrorPlan(skillName, target, feature string, tick uint64) {
	sink := s.mirrorSinkFor()
	if sink == nil {
		return
	}
	sink(mirror.Frame{
		Tick:    tick,
		Kind:    "route.plan",
		Path:    target,
		Feature: feature,
		Skill:   navtrace.ID(skillName),
		At:      time.Now().UTC(),
	})
}

// decodePathToken unwraps the base64 NegotiatedPath the negotiator returns. The
// token carries the hop isotopes, which the segment list alone does not.
func decodePathToken(best map[string]interface{}) (NegotiatedPath, error) {
	var path NegotiatedPath
	token, _ := best["token"].(string)
	if token == "" {
		return path, fmt.Errorf("negotiated path has no token")
	}
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return path, fmt.Errorf("decode path token: %w", err)
	}
	if err := json.Unmarshal(raw, &path); err != nil {
		return path, fmt.Errorf("parse path token: %w", err)
	}
	return path, nil
}
