// Package security holds the controls that keep the agent safe: secret
// redaction, mutation policy, and RBAC helpers. The model lives in the untrusted
// zone — these are the deterministic guards between it and real systems.
package security

import (
	"regexp"
	"strings"
)

// Redactor strips secret-shaped content before it is logged or returned to a
// model. It implements tools.Redactor.
type Redactor struct {
	patterns []*regexp.Regexp
}

// NewRedactor builds a redactor seeded with common credential patterns. The set
// is intentionally broad: false positives (over-redaction) are acceptable; a
// leaked secret is not.
func NewRedactor() *Redactor {
	pats := []string{
		`(?i)(authorization|bearer)\s*[:=]\s*\S+`,
		`(?i)(api[-_ ]?key|secret|token|password|passwd)\s*[:=]\s*\S+`,
		`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`, // JWT
		`AKIA[0-9A-Z]{16}`,                  // AWS access key id
		`-----BEGIN[ A-Z]+PRIVATE KEY-----`, // PEM private key
		`(?i)kubeconfig`,                    // never echo a kubeconfig
	}
	r := &Redactor{}
	for _, p := range pats {
		r.patterns = append(r.patterns, regexp.MustCompile(p))
	}
	return r
}

// Scrub redacts matches. It also collapses Kubernetes Secret data blocks: any
// line under a `data:`/`stringData:` key has its value masked.
func (r *Redactor) Scrub(s string) string {
	for _, p := range r.patterns {
		s = p.ReplaceAllString(s, "[REDACTED]")
	}
	return maskSecretData(s)
}

var secretDataLine = regexp.MustCompile(`(?m)^(\s+[\w.-]+:\s*)(\S+)\s*$`)

// maskSecretData masks values that appear under a Secret data section. This is a
// heuristic for the scaffold; the production path inspects typed objects and
// masks Secret.Data/StringData by field rather than by text.
func maskSecretData(s string) string {
	if !strings.Contains(s, "kind: Secret") {
		return s
	}
	lines := strings.Split(s, "\n")
	inData := false
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		switch {
		case trimmed == "data:" || trimmed == "stringData:":
			inData = true
		case inData && len(ln) > 0 && ln[0] != ' ':
			inData = false
		case inData:
			lines[i] = secretDataLine.ReplaceAllString(ln, "$1[REDACTED]")
		}
	}
	return strings.Join(lines, "\n")
}
