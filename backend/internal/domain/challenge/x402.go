// The x402 wire contract.
//
// Not invented here. This mirrors the Solana Foundation's pay-kit, which is the reference
// implementation of the `exact` scheme on Solana and the protocol `pay.sh` and the `pay` CLI
// speak. The spec and the cross-SDK conformance vectors are vendored under contracts/x402/, and
// the tests in this package run against those vectors — so if the protocol moves and we do not,
// a test fails rather than a customer's agent silently failing to pay.
//
// Two versions exist as PARALLEL wire shapes, not as one with a conversion. v2 is pay-kit's
// default producer; v1 is still read on the way in and emitted when the peer declared it.
package challenge

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/stone7890/leash/internal/domain/network"
)

// Version is the x402 protocol version, as a number on the wire.
type Version int

const (
	V1 Version = 1
	V2 Version = 2
)

// Header names differ between the versions, and a peer that sent one expects the other back.
const (
	HeaderPaymentV1   = "X-PAYMENT"
	HeaderChallengeV1 = "X-PAYMENT-REQUIRED"
	HeaderResponseV1  = "X-PAYMENT-RESPONSE"

	HeaderPaymentV2   = "PAYMENT-SIGNATURE"
	HeaderChallengeV2 = "PAYMENT-REQUIRED"
	HeaderResponseV2  = "PAYMENT-RESPONSE"
)

// SchemeExact is the only scheme Leash supports. Rule S6 refuses anything else rather than
// guessing what an unfamiliar one means.
const SchemeExact = "exact"

// CAIP-2 identifiers, used by v2. v1 uses plain slugs instead.
const (
	CAIP2Mainnet = "solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp"
	CAIP2Devnet  = "solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1"
	CAIP2Testnet = "solana:4uhcVJyU9pJkvQyS88uRDiswHXSCkY3z"

	SlugMainnet = "solana"
	SlugDevnet  = "solana-devnet"
	SlugTestnet = "solana-testnet"
)

// Cluster is the Solana cluster a Leash network's ledger actually lives on.
//
// It is not a fourth Leash network. Leash has two — sandbox and mainnet, invariant I8 — and this
// says which public ledger the SANDBOX's happens to be, so the x402 wire can name it correctly.
// A sandbox on devnet and a sandbox on testnet are the same Leash network with the same test_
// prefixes; they differ only in what an interoperating client must be told to build against.
type Cluster string

const (
	ClusterMainnet Cluster = "mainnet-beta"
	ClusterDevnet  Cluster = "devnet"
	ClusterTestnet Cluster = "testnet"
)

// ClusterFromGenesis names a cluster by asking the ledger which one it is.
//
// Solana's CAIP-2 identifier is `solana:` followed by the first 32 characters of the genesis hash,
// so the constants above are not a lookup table maintained alongside the truth — they ARE the
// truth, and one `getGenesisHash` decides the matter. This is the same read-back discipline as
// everywhere else: trust what the chain says, not what somebody configured.
//
// A local test validator has its own genesis and matches nothing, which is correct and is why the
// second return value exists. The caller decides what to do about that; there is no sensible
// "guess" available here.
func ClusterFromGenesis(genesis string) (Cluster, bool) {
	g := strings.TrimSpace(genesis)
	if len(g) < 32 {
		return "", false
	}
	return ClusterOf("solana:" + g[:32])
}

// ClusterOf maps either wire form of a network onto the cluster it names.
//
// Both forms must be understood: a v1 peer sends `solana-devnet`, a v2 peer sends the CAIP-2
// identifier, and the same cluster is meant. Normalising on the way in is what lets rule S0
// compare a challenge's network against an agent's without caring which version produced it.
func ClusterOf(wire string) (Cluster, bool) {
	switch strings.TrimSpace(wire) {
	case SlugMainnet, CAIP2Mainnet:
		return ClusterMainnet, true
	case SlugDevnet, CAIP2Devnet:
		return ClusterDevnet, true
	case SlugTestnet, CAIP2Testnet:
		return ClusterTestnet, true
	}
	return "", false
}

// WireNetwork renders a Leash network in the form the given protocol version uses.
//
// The protocol has no concept of "somebody's private sandbox", so the sandbox must go out as the
// public cluster its ledger actually is — which is why the cluster is an argument rather than a
// constant. Naming devnet while settling on testnet would hand every interoperating client a
// transaction it cannot build, and every mismatch would be reported as a network error rather than
// as the lie it is.
//
// A cluster that is not a public one — a local validator, whose genesis matches nothing — comes
// through as devnet. That is the honest choice among bad ones: devnet is the cluster x402
// implementations treat as "the test one", and a private ledger has no identifier of its own that
// anybody could act on.
//
// On the v1 wire testnet goes out as `solana-testnet`, which is a deliberate departure from the
// reference producer. That producer maps devnet-family to `solana-devnet` and EVERYTHING ELSE to
// `solana` (contracts/x402/exact-v1-spec.md), so a v1 offer from a testnet server would announce
// itself as mainnet. `solana-testnet` is recognised on parse by the same implementation, so saying
// the true thing costs nothing and saying the reference thing would be dangerous.
func WireNetwork(n network.Network, v Version, c Cluster) string {
	if n == network.Mainnet {
		if v == V1 {
			return SlugMainnet
		}
		return CAIP2Mainnet
	}
	if c == ClusterTestnet {
		if v == V1 {
			return SlugTestnet
		}
		return CAIP2Testnet
	}
	if v == V1 {
		return SlugDevnet
	}
	return CAIP2Devnet
}

// NetworkOf maps a wire network back onto a Leash network.
//
// Everything that is not mainnet is our sandbox. That is a deliberate narrowing: Leash has two
// worlds and the protocol has several clusters, so testnet collapses into the sandbox rather than
// becoming a third thing nobody configured.
func NetworkOf(wire string) (network.Network, bool) {
	cluster, ok := ClusterOf(wire)
	if !ok {
		return "", false
	}
	if cluster == "mainnet-beta" {
		return network.Mainnet, true
	}
	return network.Sandbox, true
}

// Offer is one entry in a challenge's `accepts` array, in either version's shape.
//
// The field tags carry the v2 names; v1's differences are handled in UnmarshalJSON, because the
// two are parallel shapes rather than one with optional fields.
type Offer struct {
	Scheme  string `json:"scheme"`
	Network string `json:"network"`

	// Amount is in BASE UNITS, as a string. Not a decimal, and not a number: the protocol says
	// "1000" for one thousandth of a six-decimal token, and a JSON number would be a double.
	// v1 calls this `maxAmountRequired`; v2 calls it `amount`.
	Amount string `json:"amount"`

	// Asset is the mint. v1 also accepts the legacy name `currency`.
	Asset string `json:"asset"`

	// PayTo is the recipient. v1 also accepts the legacy name `recipient`.
	PayTo string `json:"payTo"`

	Resource          string `json:"resource,omitempty"`
	Description       string `json:"description,omitempty"`
	MimeType          string `json:"mimeType,omitempty"`
	MaxTimeoutSeconds int    `json:"maxTimeoutSeconds,omitempty"`

	// Extra carries `feePayer` — the facilitator's key, which pays the network fee.
	//
	// This is why an agent needs no SOL of its own: the transaction is PARTIALLY signed by the
	// agent as the transfer authority, and the facilitator adds its own signature as fee payer.
	// pay-kit's verifier refuses a transaction whose fee payer IS the authority, so an agent that
	// paid its own fees would be rejected rather than merely inefficient.
	Extra map[string]any `json:"extra,omitempty"`
}

// FeePayer is the facilitator's key from `extra`, if the offer names one.
func (o Offer) FeePayer() (string, bool) {
	if o.Extra == nil {
		return "", false
	}
	v, ok := o.Extra["feePayer"].(string)
	return v, ok && v != ""
}

// Memo is the optional server-pinned memo, capped at 256 bytes by the protocol.
func (o Offer) Memo() (string, bool) {
	if o.Extra == nil {
		return "", false
	}
	v, ok := o.Extra["memo"].(string)
	return v, ok && v != ""
}

// Requirements is a whole challenge: the version, and the offers on the table.
type Requirements struct {
	Version Version `json:"x402Version"`
	Error   string  `json:"error,omitempty"`
	Accepts []Offer `json:"accepts"`
}

// PaymentEnvelope is what a client puts in the payment header.
//
// In v1, `scheme` and `network` are top-level siblings of `payload` and there is no `accepted`
// object. In v2 they are absent and `accepted` echoes the offer, which the server binds
// field-by-field. Emitting the wrong shape for the version the peer declared is how a payment gets
// refused for a reason that looks like a signature problem.
type PaymentEnvelope struct {
	Version Version `json:"x402Version"`
	Scheme  string  `json:"scheme,omitempty"`
	Network string  `json:"network,omitempty"`
	// Accepted is present in v2 only, echoing the offer being paid.
	Accepted *Offer         `json:"accepted,omitempty"`
	Payload  PaymentPayload `json:"payload"`
}

// PaymentPayload carries the partially-signed transaction, standard base64 of the bincode
// serialisation of a VersionedTransaction.
type PaymentPayload struct {
	Transaction string `json:"transaction"`
}

// SettlementResponse is what a server returns once the payment has landed.
type SettlementResponse struct {
	Success     bool   `json:"success"`
	Transaction string `json:"transaction"`
	Network     string `json:"network"`
	Payer       string `json:"payer,omitempty"`
	ErrorReason string `json:"errorReason,omitempty"`
}

// VersionSupported reports whether this build understands a version.
//
// A server MUST NOT silently accept an unknown one: it would settle a payment whose wire contract
// it does not understand. pay-kit's own server refuses, and so does this.
func VersionSupported(v Version) bool { return known(v) }

// PaymentHeaderName is the header a credential for this challenge goes in.
//
// The versions use different names, and a credential in the wrong one is simply not seen: the
// server looks for the header its own challenge declared and finds nothing.
func (c Challenge) PaymentHeaderName() string {
	if c.Version == V1 {
		return HeaderPaymentV1
	}
	return HeaderPaymentV2
}

// Credential wraps a signed transaction in the envelope the peer's version expects.
//
// v1 carries `scheme` and `network` as top-level siblings of `payload` and has no `accepted`
// object. v2 carries neither and echoes the offer in `accepted`, which the server binds
// field-by-field. Emitting the wrong shape for the declared version is refused — and refused in a
// way that looks like a signature problem rather than a formatting one.
//
// The encoding is standard PADDED base64, not base64url.
func (c Challenge) Credential(transactionBase64 string) (string, error) {
	env := PaymentEnvelope{
		Version: c.Version,
		Payload: PaymentPayload{Transaction: transactionBase64},
	}
	if c.Version == V1 {
		env.Scheme = c.Scheme
		// The cluster comes from the OFFER, not from an assumption: this credential answers a
		// specific server, and the only cluster that can be right is the one it named.
		cluster, _ := ClusterOf(c.Offer.Network)
		env.Network = WireNetwork(c.Network, V1, cluster)
	} else {
		// The offer, echoed verbatim. The server compares it against its own route, so anything
		// we alter here is a mismatch we caused.
		accepted := c.Offer
		env.Accepted = &accepted
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}
