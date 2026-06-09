// Package migrations embeds the SQL schema migrations so the binary can apply
// them at startup without shipping the .sql files separately. The files remain
// the source of truth on disk; this just makes them available to the store's
// forward-only migration runner.
package migrations

import "embed"

// FS holds every *.sql migration in this directory, applied in lexical order
// (so name them 0001_, 0002_, …).
//
//go:embed *.sql
var FS embed.FS
