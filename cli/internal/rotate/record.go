package rotate

import (
	"context"
	"fmt"
	"time"
)

// Phases a rotation records after each step, so a rerun resumes after the last one that finished.
const (
	PhasePreparing = "preparing" // Prepare started: it may have written a new value, which a rerun must find
	PhasePrepared  = "prepared"  // the new value is written
	PhaseSwitched  = "switched"  // the readers use it
	PhaseVerified  = "verified"  // the readers work with it
	PhaseDone      = "done"      // the old value is revoked: the rotation is over
)

// Record is the rotation state of one entry.
type Record struct {
	Phase  string    // "" (never rotated) or one of the phases
	At     time.Time // when the last rotation finished (zero: never)
	By     string    // the person or job
	Method string    // the entry's kind when it last rotated
	Reason string    // why the backup check was skipped, if it was
	// Since is where a never rotated entry's period starts: its value's creation, or when lab first saw it (Get
	// sets it; Set does not write it)
	Since time.Time
}

// Records keeps each entry's Record.
type Records interface {
	Get(ctx context.Context, e Entry) (Record, error)
	Set(ctx context.Context, e Entry, r Record) error
}

// MetadataStore reads and merges the custom metadata of an OpenBao KV v2 path (secrets.Store); an empty value
// removes the key. Created is a path's created_time (zero when it has none).
type MetadataStore interface {
	Created(ctx context.Context, path string) (time.Time, error)
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
	keyFirst  = "first-seen" // the baseline of an entry with no value path in OpenBao
)

// OpenBaoRecords keeps the record in the custom metadata of secret/rotation/<name>, never on the secret's own paths:
// ESO's PushSecret owns those paths' metadata (managed-by), and a generated secret's rotation may delete them.
type OpenBaoRecords struct {
	Store MetadataStore
	Now   func() time.Time // the baseline's time (default time.Now)
}

// RecordPath is where an entry's record lives, under secret/.
func RecordPath(e Entry) string { return "rotation/" + e.Name }

// Get reads the record. For a periodic entry never rotated, Since is its first value path's creation time or, without
// one, the first-seen baseline, written the first time the entry is seen.
func (o OpenBaoRecords) Get(ctx context.Context, e Entry) (Record, error) {
	path := RecordPath(e)
	m, err := o.Store.Metadata(ctx, path)
	if err != nil {
		return Record{}, err
	}
	r := Record{Phase: m[keyPhase], By: m[keyBy], Method: m[keyMethod], Reason: m[keyReason]}
	if r.At, err = parseTime(path, keyAt, m[keyAt]); err != nil {
		return Record{}, err
	}
	if !r.At.IsZero() || e.Period == 0 {
		return r, nil
	}
	if len(e.Paths) > 0 {
		if r.Since, err = o.Store.Created(ctx, e.Paths[0]); err != nil {
			return Record{}, err
		}
		if !r.Since.IsZero() {
			return r, nil
		}
	}
	if r.Since, err = parseTime(path, keyFirst, m[keyFirst]); err != nil || !r.Since.IsZero() {
		return r, err
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	r.Since = now().UTC().Truncate(time.Second)
	if err := o.Store.SetMetadata(ctx, path, map[string]string{keyFirst: r.Since.Format(time.RFC3339)}); err != nil {
		return Record{}, err
	}
	return r, nil
}

func parseTime(path, key, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("OpenBao secret/%s: %s is not an RFC 3339 time", path, key)
	}
	return t, nil
}

// Set writes every key of the record but the baseline; an empty field removes its key.
func (o OpenBaoRecords) Set(ctx context.Context, e Entry, r Record) error {
	at := ""
	if !r.At.IsZero() {
		at = r.At.UTC().Format(time.RFC3339)
	}
	return o.Store.SetMetadata(ctx, RecordPath(e), map[string]string{
		keyPhase: r.Phase, keyAt: at, keyBy: r.By, keyMethod: r.Method, keyReason: r.Reason,
	})
}
