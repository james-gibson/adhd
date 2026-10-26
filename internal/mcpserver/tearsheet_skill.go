package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/james-gibson/adhd/internal/proxy"
	"github.com/james-gibson/adhd/internal/tearsheet"
)

// ProxyHopTearSkillName is the dynamic skill that tears one hop off a proxy
// tear sheet and executes it.
const ProxyHopTearSkillName = "adhd.skill.proxy-hop-tear.call"

// RegisterProxyHopTearSkill installs the skill under its well-known name.
func (s *Server) RegisterProxyHopTearSkill() {
	s.RegisterProxyHopTearSkillAs(ProxyHopTearSkillName)
}

// RegisterProxyHopTearSkillAs installs the skill under name. It is a dynamic
// tool, so it appears in the surface snapshot for the tick it is registered in
// and disappears on rotation like any other skill.
func (s *Server) RegisterProxyHopTearSkillAs(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.dynamicTools {
		if t.name == name {
			return
		}
	}
	s.dynamicTools = append(s.dynamicTools, &dynamicTool{
		name:        name,
		description: "Tear one hop off a proxy tear sheet and execute it. Each hop is single-use: a redeemed ticket is refused and alerted, not retried.",
		inputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"sheet": map[string]interface{}{
					"type":        "object",
					"description": "The tear sheet: id, tick and the hops not yet torn.",
				},
				"call": map[string]interface{}{
					"type":        "object",
					"description": "The JSON-RPC call to forward through the torn hop.",
				},
			},
			"required": []string{"sheet", "call"},
		},
		handler: func(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
			return s.tearProxyHop(ctx, name, params)
		},
	})
}

// tearProxyHop validates the sheet against the public tick, spends the ticket
// nonce, then forwards the call to the hop's endpoint.
//
// The nonce is spent before the forward. A hop that fails upstream is still
// burned: letting a caller repeat a hop until it succeeds is exactly the retry
// loop the single-use ticket exists to prevent.
func (s *Server) tearProxyHop(ctx context.Context, skillName string, params interface{}) (interface{}, *jsonrpcError) {
	m, ok := params.(map[string]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "params must be an object"}
	}
	sheetRaw, ok := m["sheet"].(map[string]interface{})
	if !ok || len(sheetRaw) == 0 {
		return nil, &jsonrpcError{Code: -32602, Message: "sheet is required and must be an object"}
	}
	call, ok := m["call"].(map[string]interface{})
	if !ok || len(call) == 0 {
		return nil, &jsonrpcError{Code: -32602, Message: "call is required and must be an object"}
	}

	encoded, err := json.Marshal(sheetRaw)
	if err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: "sheet is not encodable: " + err.Error()}
	}
	var sheet tearsheet.Sheet
	if err := json.Unmarshal(encoded, &sheet); err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: "sheet is not a tear sheet: " + err.Error()}
	}

	now, ticked := s.nowTick()
	if !ticked {
		return nil, &jsonrpcError{Code: -32603, Message: "no tick source installed"}
	}

	ticket, rest, err := tearsheet.Tear(sheet, now, PathGraceTicks)
	if err != nil {
		switch {
		case errors.Is(err, tearsheet.ErrStale):
			s.fireSecurity("warn", "path_stale", map[string]interface{}{
				"source": "tearsheet",
				"sheet":  sheet.ID,
				"tick":   sheet.Tick,
				"now":    now,
			})
			return nil, &jsonrpcError{
				Code: -32010,
				Message: fmt.Sprintf("tear sheet %s expired: issued for tick %d, now %d (grace %d tick)",
					sheet.ID, sheet.Tick, now, PathGraceTicks),
			}
		case errors.Is(err, tearsheet.ErrFuture):
			return nil, &jsonrpcError{
				Code: -32602,
				Message: fmt.Sprintf("tear sheet %s is not valid yet: issued for tick %d, now %d",
					sheet.ID, sheet.Tick, now),
			}
		default:
			return nil, &jsonrpcError{Code: -32602, Message: err.Error()}
		}
	}

	ledger := s.tearLedgerFor()
	if ledger == nil {
		return nil, &jsonrpcError{Code: -32603, Message: "no tear ledger installed"}
	}
	if err := ledger.Redeem(ticket.Nonce, ticket.Tick, now, PathGraceTicks); err != nil {
		s.fireSecurity("warn", "skill_reuse", map[string]interface{}{
			"tool":  skillName,
			"sheet": ticket.Sheet,
			"nonce": ticket.Nonce,
			"hop":   ticket.Index,
		})
		return nil, &jsonrpcError{
			Code: -32000,
			Message: fmt.Sprintf("tear sheet %s ticket %d already used: hops are single-use",
				ticket.Sheet, ticket.Index),
		}
	}

	resp, execErr := s.proxyExecutor.ExecuteProxy(ctx, proxy.ProxyRequest{
		TargetEndpoint: ticket.Hop.Endpoint,
		Call:           call,
	})
	if execErr != nil {
		return nil, &jsonrpcError{Code: -32603, Message: execErr.Error()}
	}
	if resp.Error != nil {
		return nil, &jsonrpcError{Code: resp.Error.Code, Message: resp.Error.Message}
	}
	// A refusal at the hop arrives inside the nested JSON-RPC response, the
	// same shape adhd.path.call propagates.
	if nested, ok := resp.Result.(map[string]interface{}); ok {
		if errObj, ok := nested["error"].(map[string]interface{}); ok {
			code, _ := errObj["code"].(float64)
			message, _ := errObj["message"].(string)
			return nil, &jsonrpcError{Code: int(code), Message: message}
		}
	}

	return map[string]interface{}{
		"sheet":     rest,
		"ticket":    ticket,
		"remaining": len(rest.Hops),
		"result":    resp.Result,
	}, nil
}
