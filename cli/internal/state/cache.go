package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// minCachedLifetime is how long a cached credential must still last to be reused: enough for a long apply.
const minCachedLifetime = 15 * time.Minute

// CachedR2Credentials reuses a credential cached under dir (the user's runtime directory: memory-backed, user-only)
// while it lasts minCachedLifetime, so each OpenTofu process does not create its own account token. Without dir it
// always creates one. The cache is best effort: an unreadable or unwritable file only means a new token.
func CachedR2Credentials(dir, account, bucket string, now time.Time, create func() (R2Credentials, error)) (R2Credentials, error) {
	if dir == "" {
		return create()
	}
	cacheDir := filepath.Join(dir, "homelab-lab")
	path := filepath.Join(cacheDir, "r2-"+account+"-"+bucket+".json")
	if raw, err := os.ReadFile(path); err == nil {
		var cached R2Credentials
		if json.Unmarshal(raw, &cached) == nil && cached.Expiration.Sub(now) >= minCachedLifetime {
			return cached, nil
		}
	}
	credentials, err := create()
	if err != nil {
		return R2Credentials{}, err
	}
	if raw, err := json.Marshal(credentials); err == nil && os.MkdirAll(cacheDir, 0o700) == nil {
		if tmp, err := os.CreateTemp(cacheDir, ".r2-*"); err == nil {
			_, werr := tmp.Write(raw)
			if cerr := tmp.Close(); werr == nil && cerr == nil {
				_ = os.Rename(tmp.Name(), path)
			}
			_ = os.Remove(tmp.Name())
		}
	}
	return credentials, nil
}
