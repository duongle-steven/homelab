package rotate

import (
	"context"
	"fmt"
	"time"
)

// Phases a rotation records after each step, so a rerun resumes after the last one that finished.
const (
	PhasePrepared = "prepared" // the new value is written
	PhaseSwitched = "switched" // the readers use it
	PhaseVerified = "verified" // the readers work with it
	PhaseDone     = "done"     // the old value is revoked: the rotation is over
)

// Record is the rotation state of one entry.
type Record struct {
	Phase  string    // "" (never rotated) or one of the phases
	At     time.Time // when the last rotation finished (zero: never)
	By     string    // the person or job
	Method string    // the entry's kind when it last rotated
	Reason string    // why the backup check was skipped, if it was
}

// Records keeps each entry's Record.
type Records interface {
	Get(ctx context.Context, e Entry) (Record, error)
	Set(ctx context.Context, e Entry, r Record) error
}

// MetadataStore reads and merges the custom metadata of an OpenBao KV v2 path (secrets.Store); an empty value
// removes the key.
type MetadataStore interface {
	Metadata(ctx context.Context, path string) (map[string]string, error)
	SetMetadata(ctx context.Context, path string, set map[string]string) error
}

// Keys of the record in the custom metadata.
const (
	keyPhase  = "rotation-phase"
	keyAt     = "rotated-at"
	keyBy     = "rotated-by"
	keyMethod = "rotation-method"
	keyReason = "rotation-reason"
)

// OpenBaoRecords keeps the record in the custom metadata of the entry's first OpenBao path, beside its value's
// versions, or of secret/rotation/<name> for an entry without one (OpenTofu's, GitHub's). Other metadata keys
// (ESO's managed-by) are kept.
type OpenBaoRecords struct{ Store MetadataStore }

// RecordPath is where an entry's record lives, under secret/.
func RecordPath(e Entry) string {
	if len(e.Paths) > 0 {
		return e.Paths[0]
	}
	return "rotation/" + e.Name
}

// Get reads the record; an entry never rotated has the zero Record.
func (o OpenBaoRecords) Get(ctx context.Context, e Entry) (Record, error) {
	path := RecordPath(e)
	m, err := o.Store.Metadata(ctx, path)
	if err != nil {
		return Record{}, err
	}
	r := Record{Phase: m[keyPhase], By: m[keyBy], Method: m[keyMethod], Reason: m[keyReason]}
	if at := m[keyAt]; at != "" {
		if r.At, err = time.Parse(time.RFC3339, at); err != nil {
			return Record{}, fmt.Errorf("OpenBao secret/%s: %s is not an RFC 3339 time", path, keyAt)
		}
	}
	return r, nil
}

// Set writes every key of the record; an empty field removes its key.
func (o OpenBaoRecords) Set(ctx context.Context, e Entry, r Record) error {
	at := ""
	if !r.At.IsZero() {
		at = r.At.UTC().Format(time.RFC3339)
	}
	return o.Store.SetMetadata(ctx, RecordPath(e), map[string]string{
		keyPhase: r.Phase, keyAt: at, keyBy: r.By, keyMethod: r.Method, keyReason: r.Reason,
	})
}
