// Package store is the persistence layer. It implements session.Store (messages
// + long-term memory) and tools.AuditSink (hash-chained audit log + the
// tool_executions operational record), plus session/cluster/user/finding/usage
// access. The SQLite driver (modernc, pure Go — no cgo) backs the local CLI;
// a Postgres driver behind the same methods backs the server build.
package store

import (
	"database/sql"
	"encoding/binary"
	"math"
)

// nullStr maps "" to SQL NULL so optional FKs/columns stay clean.
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// encodeVector packs a float32 embedding into a little-endian byte blob for
// storage. (Postgres uses a native pgvector column instead.)
func encodeVector(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func decodeVector(b []byte) []float32 {
	n := len(b) / 4
	if n == 0 {
		return nil
	}
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// cosine returns the cosine similarity of two equal-length vectors in [-1,1].
// Used for brute-force memory recall at local scale.
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
