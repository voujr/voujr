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
	rs.Register(MissingReadinessProbe{})
	rs.Register(MissingLivenessProbe{})
	// security.privileged_container, cost.overprovisioned_cpu,
	// optimization.idle_workload, … register here.
}
