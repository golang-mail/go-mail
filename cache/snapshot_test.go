package cache

import "testing"

func TestSnapshotIsolatesMutableSlices(t *testing.T) {
	orig := &CachedEmail{
		UID:        7,
		Envelope:   &Envelope{Subject: "s"},
		Flags:      []string{`\Seen`},
		Labels:     []string{"a"},
		PluginsRun: []string{"p"},
	}
	snap := orig.Snapshot()

	orig.Flags[0] = `\Deleted`
	orig.Labels = append(orig.Labels, "b")
	orig.PluginsRun = nil

	if snap.Flags[0] != `\Seen` || len(snap.Labels) != 1 || len(snap.PluginsRun) != 1 {
		t.Fatalf("snapshot shares storage with original: %+v", snap)
	}
	if snap.UID != 7 || snap.Envelope != orig.Envelope {
		t.Fatal("snapshot must keep scalar fields and share the immutable envelope")
	}
}
