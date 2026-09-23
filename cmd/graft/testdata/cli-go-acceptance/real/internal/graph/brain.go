package graph

const maxAttachedRules = 6

// BrainRule is a cached rule anchored to a graph node.
type BrainRule struct {
	RuleID      string `json:"ruleId"`
	Symbol      string `json:"symbol"`
	Fingerprint string `json:"fingerprint"`
	Rule        string `json:"rule"`
	SourceURL   string `json:"sourceUrl,omitempty"`
}

// AppliedRule is a brain rule resolved against a current graph pointer.
type AppliedRule struct {
	Rule      string `json:"rule"`
	Pointer   string `json:"pointer"`
	Stale     bool   `json:"stale"`
	SourceURL string `json:"sourceUrl,omitempty"`
}

// ApplyBrainRules resolves cached rules for the pointers in one Ask result.
func ApplyBrainRules(pointers []string, rules []BrainRule, wiring *GraphV1) []AppliedRule {
	if len(rules) == 0 || wiring == nil {
		return nil
	}

	type nodeState struct {
		pointer string
		hash    string
	}
	byID := make(map[string]nodeState, len(wiring.Nodes))
	for _, node := range wiring.Nodes {
		byID[node.ID] = nodeState{pointer: node.Path + ":" + node.Span, hash: node.BodyHash}
	}
	wanted := make(map[string]struct{}, len(pointers))
	for _, pointer := range pointers {
		wanted[pointer] = struct{}{}
	}

	applied := make([]AppliedRule, 0, min(maxAttachedRules, len(rules)))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		node, ok := byID[rule.Symbol]
		if !ok {
			continue
		}
		if _, ok := wanted[node.pointer]; !ok {
			continue
		}
		if _, ok := seen[rule.Rule]; ok {
			continue
		}
		seen[rule.Rule] = struct{}{}
		applied = append(applied, AppliedRule{
			Rule:      rule.Rule,
			Pointer:   node.pointer,
			Stale:     rule.Fingerprint != "" && rule.Fingerprint != node.hash,
			SourceURL: rule.SourceURL,
		})
		if len(applied) == maxAttachedRules {
			break
		}
	}
	if len(applied) == 0 {
		return nil
	}
	return applied
}
