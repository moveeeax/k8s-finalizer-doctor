package scan

import (
	"encoding/json"
	"fmt"
	"os"
)

// LoadSnapshot reads a snapshot fixture, the same shape --dump writes.
func LoadSnapshot(path string) (Snapshot, error) {
	var s Snapshot
	raw, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// SaveSnapshot writes a snapshot so a cluster state can be replayed offline,
// attached to a ticket, or turned into a regression fixture.
func SaveSnapshot(path string, s Snapshot) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// FilterNamespace keeps only objects in ns (plus cluster-scoped ones when ns is
// empty). The inventory of CRDs, APIServices and Deployments stays whole: the
// owner of a finalizer usually lives in a different namespace than the object
// it is stuck on.
func FilterNamespace(s Snapshot, ns string) Snapshot {
	if ns == "" {
		return s
	}
	out := s
	out.Objects = nil
	for _, o := range s.Objects {
		if o.Namespace == ns {
			out.Objects = append(out.Objects, o)
		}
	}
	return out
}
