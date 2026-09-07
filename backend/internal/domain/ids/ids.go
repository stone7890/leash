// Package ids mints and parses the identifiers used throughout Leash.
//
// An identifier is a kind, optionally a network, and a ULID:
//
//	org_01J8XKQ2M4Z7YB3F9C5R6D8W1H
//	agt_test_01J8XKQ2M4Z7YB3F9C5R6D8W1H     agt_live_01J8XKQ2M4Z7YB3F9C5R6D8W1H
//
// Two decisions are load-bearing.
//
// The network is part of the identifier because MongoDB refuses any update that modifies _id.
// That makes a record's network immutable at the storage engine — strictly stronger than the
// trigger the specification asked for — and it makes a mainnet document referencing a sandbox one
// detectable by string comparison rather than a join. Invariant I8.
//
// The suffix is a ULID rather than a UUID because it is time-sortable and monotonic within a
// millisecond, and FIFO is defined against it. BSON dates are millisecond resolution, so "FIFO by
// signing time" is not a total order under contention; the order requests reach the allowance
// compare-and-swap is, and the ULID is how that order is reconstructed. See
// docs/04-policy-engine.md.
//
// What is lost by leaving `bigserial` behind: the sequence is GAPPED, so an audit trail's
// completeness cannot be proved by counting. Registered in docs/16-deck-conformance.md §B-5.
package ids

import (
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/stone7890/leash/internal/domain/network"
)

// Kind is the prefix that says what an identifier refers to.
type Kind string

const (
	KindOrg       Kind = "org"
	KindAgent     Kind = "agt"
	KindAllowance Kind = "alw"
	KindPayment   Kind = "pay"
	KindSignReq   Kind = "sgr"
	KindHandshake Kind = "hse"
	KindRevision  Kind = "rev"
	KindAlert     Kind = "alr"
	KindJob       Kind = "job"
	KindIntent    Kind = "int"
	KindTemplate  Kind = "tpl"
)

// networkScoped lists the kinds that carry a network segment. These are exactly the collections
// invariant I8 applies to: an identifier without a network cannot be checked against one.
var networkScoped = map[Kind]bool{
	KindAgent:     true,
	KindAllowance: true,
	KindPayment:   true,
	KindSignReq:   true,
}

func (k Kind) NetworkScoped() bool { return networkScoped[k] }

var (
	ErrMalformed   = errors.New("identifier is malformed")
	ErrWrongKind   = errors.New("identifier is of the wrong kind")
	ErrNoNetwork   = errors.New("identifier of this kind carries no network")
	ErrNeedNetwork = errors.New("identifier of this kind requires a network")
)

// ID is a parsed, valid identifier. The zero value is not valid.
type ID struct {
	kind Kind
	net  network.Network // empty for kinds that are not network-scoped
	ulid string
	s    string
}

func (i ID) String() string               { return i.s }
func (i ID) Kind() Kind                   { return i.kind }
func (i ID) ULID() string                 { return i.ulid }
func (i ID) IsZero() bool                 { return i.s == "" }
func (i ID) MarshalJSON() ([]byte, error) { return []byte(`"` + i.s + `"`), nil }

func (i *ID) UnmarshalJSON(p []byte) error {
	if len(p) < 2 || p[0] != '"' || p[len(p)-1] != '"' {
		return ErrMalformed
	}
	v, err := Parse(string(p[1 : len(p)-1]))
	if err != nil {
		return err
	}
	*i = v
	return nil
}

// Network returns the network this identifier belongs to. It is an error to ask an identifier that
// does not carry one, rather than returning a zero value a caller might compare against something.
func (i ID) Network() (network.Network, error) {
	if !i.kind.NetworkScoped() {
		return "", ErrNoNetwork
	}
	return i.net, nil
}

// SameNetwork reports whether two identifiers belong to the same network.
//
// This is the check that I8 turns into a string comparison. A mainnet payment referencing a
// sandbox agent is caught here, and by the database validator, without a join.
func SameNetwork(a, b ID) bool {
	if !a.kind.NetworkScoped() || !b.kind.NetworkScoped() {
		return false
	}
	return a.net == b.net
}

// New mints an identifier for a kind that carries no network.
func New(k Kind) (ID, error) {
	if k.NetworkScoped() {
		return ID{}, ErrNeedNetwork
	}
	u := newULID()
	return ID{kind: k, ulid: u, s: string(k) + "_" + u}, nil
}

// NewOn mints an identifier for a network-scoped kind.
//
// The network must be passed explicitly. There is no variant that infers it from configuration,
// because a default network is the one thing capable of creating a mainnet record during a sandbox
// operation.
func NewOn(k Kind, n network.Network) (ID, error) {
	if !k.NetworkScoped() {
		return ID{}, ErrNoNetwork
	}
	if !n.Valid() {
		return ID{}, network.ErrUnknown
	}
	u := newULID()
	return ID{kind: k, net: n, ulid: u, s: string(k) + "_" + n.IDPrefix() + "_" + u}, nil
}

// Parse validates an identifier completely: the kind is known, the network segment is present
// exactly when the kind requires it, and the ULID is well formed.
func Parse(s string) (ID, error) {
	parts := strings.Split(s, "_")
	if len(parts) < 2 || len(parts) > 3 {
		return ID{}, ErrMalformed
	}
	k := Kind(parts[0])
	if !knownKind(k) {
		return ID{}, ErrMalformed
	}

	// Templates keep their text primary key from the specification: tpl_research, tpl_scraper,
	// tpl_blank. They are seeded, not minted, so they are the one kind with a readable suffix.
	if k == KindTemplate {
		if len(parts) != 2 || parts[1] == "" {
			return ID{}, ErrMalformed
		}
		return ID{kind: k, ulid: parts[1], s: s}, nil
	}

	if k.NetworkScoped() {
		if len(parts) != 3 {
			return ID{}, ErrMalformed
		}
		n, err := network.FromIDPrefix(parts[1])
		if err != nil {
			return ID{}, ErrMalformed
		}
		if err := validULID(parts[2]); err != nil {
			return ID{}, err
		}
		return ID{kind: k, net: n, ulid: parts[2], s: s}, nil
	}

	if len(parts) != 2 {
		return ID{}, ErrMalformed
	}
	if err := validULID(parts[1]); err != nil {
		return ID{}, err
	}
	return ID{kind: k, ulid: parts[1], s: s}, nil
}

// ParseKind is Parse with an assertion about what was expected. Handlers use it so that passing an
// agent identifier where a payment identifier belongs is a 404 rather than a confusing miss.
func ParseKind(s string, want Kind) (ID, error) {
	id, err := Parse(s)
	if err != nil {
		return ID{}, err
	}
	if id.kind != want {
		return ID{}, ErrWrongKind
	}
	return id, nil
}

func knownKind(k Kind) bool {
	switch k {
	case KindOrg, KindAgent, KindAllowance, KindPayment, KindSignReq,
		KindHandshake, KindRevision, KindAlert, KindJob, KindIntent, KindTemplate:
		return true
	}
	return false
}

// Crockford base32, as ULID specifies: no I, L, O or U, so a transcribed identifier cannot be
// misread.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func validULID(s string) error {
	if len(s) != 26 {
		return ErrMalformed
	}
	for _, r := range s {
		if !strings.ContainsRune(crockford, r) {
			return ErrMalformed
		}
	}
	return nil
}

var mono struct {
	sync.Mutex
	lastMS  uint64
	lastRnd [10]byte
}

// newULID builds a ULID that is monotonic within a millisecond.
//
// Monotonicity is not cosmetic here: FIFO is reconstructed from these, so two identifiers minted
// in the same millisecond must still sort in the order they were minted. Within a millisecond the
// random component is incremented rather than redrawn, which is what the ULID specification calls
// for and what makes the sort stable.
func newULID() string {
	now := uint64(time.Now().UnixMilli())

	mono.Lock()
	var rnd [10]byte
	if now == mono.lastMS {
		rnd = mono.lastRnd
		for i := len(rnd) - 1; i >= 0; i-- {
			rnd[i]++
			if rnd[i] != 0 {
				break
			}
		}
	} else {
		if _, err := rand.Read(rnd[:]); err != nil {
			// crypto/rand does not fail on any supported platform; if it ever does, continuing
			// with a predictable identifier would be worse than stopping.
			panic("ids: crypto/rand unavailable: " + err.Error())
		}
		mono.lastMS = now
	}
	mono.lastRnd = rnd
	mono.Unlock()

	var out [26]byte
	// 48 bits of timestamp across the first ten characters.
	for i := 9; i >= 0; i-- {
		out[i] = crockford[now&31]
		now >>= 5
	}
	// 80 bits of randomness across the remaining sixteen.
	var acc, bits uint32
	pos := 10
	for _, b := range rnd {
		acc = acc<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[pos] = crockford[(acc>>bits)&31]
			pos++
		}
	}
	return string(out[:])
}
