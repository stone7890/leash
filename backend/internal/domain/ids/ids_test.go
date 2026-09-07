package ids

import (
	"sort"
	"testing"

	"github.com/stone7890/leash/internal/domain/network"
)

func TestANetworkScopedIdentifierCarriesItsNetwork(t *testing.T) {
	id, err := NewOn(KindAgent, network.Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if got := id.String()[:9]; got != "agt_test_" {
		t.Errorf("prefix = %q, want agt_test_", got)
	}
	n, err := id.Network()
	if err != nil || n != network.Sandbox {
		t.Errorf("Network() = %v, %v", n, err)
	}
}

func TestAnIdentifierWithoutANetworkRefusesToInventOne(t *testing.T) {
	id, err := New(KindOrg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := id.Network(); err != ErrNoNetwork {
		t.Errorf("Network() on an org should be ErrNoNetwork, got %v", err)
	}
}

// The mistake this prevents: minting a network-scoped identifier from a context that has no
// network, and silently defaulting to one.
func TestMintingRefusesTheWrongConstructor(t *testing.T) {
	if _, err := New(KindAgent); err != ErrNeedNetwork {
		t.Errorf("New(agent) should be ErrNeedNetwork, got %v", err)
	}
	if _, err := NewOn(KindOrg, network.Sandbox); err != ErrNoNetwork {
		t.Errorf("NewOn(org) should be ErrNoNetwork, got %v", err)
	}
}

// I8, as a string comparison. This is the check a composite foreign key would have made.
func TestSameNetworkCatchesACrossNetworkReference(t *testing.T) {
	agent, _ := NewOn(KindAgent, network.Sandbox)
	sandboxPay, _ := NewOn(KindPayment, network.Sandbox)
	mainnetPay, _ := NewOn(KindPayment, network.Mainnet)

	if !SameNetwork(agent, sandboxPay) {
		t.Error("two sandbox identifiers should match")
	}
	if SameNetwork(agent, mainnetPay) {
		t.Error("a mainnet payment must not match a sandbox agent")
	}
}

func TestParseRoundTripsEveryKind(t *testing.T) {
	for _, k := range []Kind{KindOrg, KindAlert, KindJob, KindRevision, KindHandshake, KindIntent} {
		id, err := New(k)
		if err != nil {
			t.Fatalf("New(%s): %v", k, err)
		}
		back, err := Parse(id.String())
		if err != nil || back.String() != id.String() || back.Kind() != k {
			t.Errorf("round trip of %s failed: %v", id, err)
		}
	}
	for _, k := range []Kind{KindAgent, KindAllowance, KindPayment, KindSignReq} {
		for _, n := range network.All() {
			id, _ := NewOn(k, n)
			back, err := Parse(id.String())
			if err != nil || back.String() != id.String() {
				t.Errorf("round trip of %s failed: %v", id, err)
			}
			if gn, _ := back.Network(); gn != n {
				t.Errorf("%s parsed as network %v, want %v", id, gn, n)
			}
		}
	}
}

func TestParseRefusesMalformedIdentifiers(t *testing.T) {
	for _, s := range []string{
		"", "agt", "agt_", "xyz_01J8XKQ2M4Z7YB3F9C5R6D8W1H",
		"agt_01J8XKQ2M4Z7YB3F9C5R6D8W1H",       // network-scoped, no network
		"org_test_01J8XKQ2M4Z7YB3F9C5R6D8W1H",  // not network-scoped, has one
		"agt_prod_01J8XKQ2M4Z7YB3F9C5R6D8W1H",  // unknown network segment
		"agt_test_01J8XKQ2M4Z7YB3F9C5R6D8W1",   // 25 characters
		"agt_test_01J8XKQ2M4Z7YB3F9C5R6D8W1HH", // 27
		"agt_test_01J8XKQ2M4Z7YB3F9C5R6D8W1I",  // I is not in Crockford base32
		"agt_test_01J8XKQ2M4Z7YB3F9C5R6D8W1L",  // nor L
		"agt_test_01J8XKQ2M4Z7YB3F9C5R6D8W1U",  // nor U
	} {
		if id, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) = %v, want an error", s, id)
		}
	}
}

func TestParseKindRefusesTheWrongKind(t *testing.T) {
	id, _ := NewOn(KindAgent, network.Sandbox)
	if _, err := ParseKind(id.String(), KindPayment); err != ErrWrongKind {
		t.Errorf("want ErrWrongKind, got %v", err)
	}
}

func TestTemplatesKeepTheirReadableKeys(t *testing.T) {
	for _, s := range []string{"tpl_research", "tpl_scraper", "tpl_blank"} {
		id, err := Parse(s)
		if err != nil || id.Kind() != KindTemplate {
			t.Errorf("Parse(%q) = %v, %v", s, id, err)
		}
	}
}

// FIFO is reconstructed from these, so identifiers minted inside one millisecond must still sort
// in the order they were minted. A redrawn random component would sort arbitrarily.
func TestIdentifiersMintedInOneMillisecondStillSortInOrder(t *testing.T) {
	const n = 500
	got := make([]string, n)
	for i := range got {
		id, err := NewOn(KindSignReq, network.Sandbox)
		if err != nil {
			t.Fatal(err)
		}
		got[i] = id.String()
	}
	sorted := append([]string(nil), got...)
	sort.Strings(sorted)
	for i := range got {
		if got[i] != sorted[i] {
			t.Fatalf("identifier %d is out of order: minted %s, sorted %s", i, got[i], sorted[i])
		}
	}
	seen := map[string]bool{}
	for _, s := range got {
		if seen[s] {
			t.Fatalf("duplicate identifier %s", s)
		}
		seen[s] = true
	}
}
