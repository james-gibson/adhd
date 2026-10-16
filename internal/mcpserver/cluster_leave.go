package mcpserver

import (
	"strings"
)

// handleClusterLeave removes a cluster peer and any dynamic tools registered by it.
func (s *Server) handleClusterLeave(params interface{}) (interface{}, *jsonrpcError) {
	m, _ := params.(map[string]interface{})
	name := firstString(m, "name")
	if name == "" {
		return nil, &jsonrpcError{Code: -32602, Message: "name is required"}
	}

	removed := 0
	s.mu.Lock()
	// Remove peer if exists
	if _, ok := s.clusterPeers[name]; ok {
		delete(s.clusterPeers, name)
		removed++
	}

	// Remove dynamic tools registered by this cluster.
	// Tools include: adhd.project.<name>.call, adhd.dependabot.<owner>.<repo>, and honeypots.
	// We identify by cluster via description? Or track cluster->tools. Simpler: for
	// per-cluster registered tools, descriptions include "(cluster: %s)". Also honeypots
	// don't include cluster in desc currently - but we log them. Better to track mappings.
	// For now, filter out tools that were registered for this cluster name.
	// However we don't store back-reference. Alternative: remove tools whose name contains
	// the cluster name for project tools? Or be conservative.
	filtered := make([]*dynamicTool, 0, len(s.dynamicTools))
	for _, t := range s.dynamicTools {
		keep := true
		// Prefer explicit cluster tracking if present
		if t.cluster != "" && t.cluster == name {
			keep = false
		}
		// Fallback to description heuristic for backward compatibility
		if keep && strings.Contains(t.description, "(cluster: "+name+")") {
			keep = false
		}
		if !keep {
			removed++
			continue
		}
		filtered = append(filtered, t)
	}
	s.dynamicTools = filtered
	s.mu.Unlock()

	return map[string]interface{}{
		"removed":  removed > 0,
		"count":    removed,
		"peer":     name,
		"accepted": true,
	}, nil
}
