package policy

import "github.com/qosi/agentbox/internal/config"

type EffectivePolicy struct {
	Config config.Policy
}

func BuildEffectivePolicy(cfg config.Policy) EffectivePolicy {
	return EffectivePolicy{Config: cfg}
}
