package rules

import (
	"time"

	"github.com/voujr/voujr/internal/audit"
)

// timeNow is indirected so tests can freeze the clock.
var timeNow = time.Now

// RegisterAll wires the built-in rule library into a rule set. New rules are
// added here as they are implemented across the reliability/security/cost/
// optimization categories.
func RegisterAll(rs *audit.RuleSet) {
	// reliability
	rs.Register(MissingReadinessProbe{})
	rs.Register(MissingLivenessProbe{})
	// security
	rs.Register(PrivilegedContainer{})
	rs.Register(HostPathVolume{})
	// cost
	rs.Register(MissingResourceRequests{})
	// optimization
	rs.Register(MissingMemoryLimit{})
	// Future rules (need a richer snapshot: RBAC, Secrets, NetworkPolicies,
	// live metrics for right-sizing/idle detection) register here.
}
