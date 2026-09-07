package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A migration whose name no longer describes it is worse than one with no name at all: it is a
// claim about what the file does, and a reader who trusts it stops checking.
func TestEveryFileNameMatchesItsMigrationName(t *testing.T) {
	files, err := filepath.Glob("0*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no migration files found")
	}

	byVersion := map[int]Migration{}
	for _, m := range All() {
		byVersion[m.Version] = m
	}

	seen := map[int]bool{}
	for _, f := range files {
		base := strings.TrimSuffix(filepath.Base(f), ".go")
		num, name, ok := strings.Cut(base, "_")
		if !ok {
			t.Errorf("%s is not named NNNN_a_sentence.go", f)
			continue
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			t.Errorf("%s does not start with a version number", f)
			continue
		}
		m, registered := byVersion[v]
		if !registered {
			t.Errorf("%s exists but registers no migration with version %d", f, v)
			continue
		}
		if m.Name != name {
			t.Errorf("%s declares Name %q — the file name and the Name field must agree", f, m.Name)
		}
		seen[v] = true
	}
	for v, m := range byVersion {
		if !seen[v] {
			t.Errorf("migration %d (%s) is registered but has no file named %04d_%s.go", v, m.Name, v, m.Name)
		}
	}
}

// Gaps are permitted — a migration may be withdrawn before release — but duplicates and
// out-of-order registration are not, because Apply relies on the order being total.
func TestVersionsAreUniqueAndOrdered(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("no migrations registered")
	}
	seen := map[int]string{}
	last := 0
	for _, m := range all {
		if prev, dup := seen[m.Version]; dup {
			t.Errorf("version %d is used by both %q and %q", m.Version, prev, m.Name)
		}
		seen[m.Version] = m.Name
		if m.Version <= last {
			t.Errorf("All() returned %d after %d — the order must be total", m.Version, last)
		}
		last = m.Version
	}
}

// Every migration must reverse. CI proves it against a real server as well, by applying every Up,
// snapshotting, applying every Down, asserting nothing survives, and applying every Up again —
// but a nil Down is worth catching without waiting for a container to start.
func TestEveryMigrationHasBothDirections(t *testing.T) {
	for _, m := range All() {
		if m.Up == nil {
			t.Errorf("migration %d (%s) has no Up", m.Version, m.Name)
		}
		if m.Down == nil {
			t.Errorf("migration %d (%s) has no Down — every migration must reverse", m.Version, m.Name)
		}
	}
}

// The name is a sentence, not a label: lower case, underscore separated, and long enough to say
// something. "0005_fix" tells a future reader nothing.
func TestNamesReadAsSentences(t *testing.T) {
	for _, m := range All() {
		if m.Name != strings.ToLower(m.Name) {
			t.Errorf("%d: %q should be lower case", m.Version, m.Name)
		}
		if strings.Contains(m.Name, " ") || strings.Contains(m.Name, "-") {
			t.Errorf("%d: %q should separate words with underscores", m.Version, m.Name)
		}
		if words := strings.Count(m.Name, "_") + 1; words < 4 {
			t.Errorf("%d: %q is %d words — a migration name should state the rule it establishes",
				m.Version, m.Name, words)
		}
	}
}

// The checksum detects a migration edited after it was applied. It covers the version and the
// name, and deliberately NOT the function bodies, which Go cannot hash at runtime — so this test
// documents the limit as much as it checks the behaviour.
func TestTheChecksumChangesWithTheName(t *testing.T) {
	a := checksum(Migration{Version: 5, Name: "one_live_allowance_per_agent_and_mint"})
	b := checksum(Migration{Version: 5, Name: "one_live_allowance_per_agent"})
	c := checksum(Migration{Version: 6, Name: "one_live_allowance_per_agent_and_mint"})
	if a == b {
		t.Error("renaming a migration did not change its checksum")
	}
	if a == c {
		t.Error("renumbering a migration did not change its checksum")
	}
	if len(a) != 64 {
		t.Errorf("checksum is %d characters, want 64", len(a))
	}
}

func TestMigrationFilesExistOnDisk(t *testing.T) {
	for _, m := range All() {
		f := fmt.Sprintf("%04d_%s.go", m.Version, m.Name)
		if _, err := os.Stat(f); err != nil {
			t.Errorf("migration %d has no file at %s", m.Version, f)
		}
	}
}
