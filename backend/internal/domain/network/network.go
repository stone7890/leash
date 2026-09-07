// Package network is the sandbox/mainnet axis.
//
// Invariant I8: sandbox and mainnet are absolutely isolated. The network is fixed on a record when
// it is created and can never change, API keys carry a prefix that matches it, and the RPC
// endpoint is chosen from the record rather than from configuration.
//
// The rule this package exists to make structural: THERE IS NO CURRENT NETWORK. No global, no
// default, no "the one we are configured for". A Network is always a value that came from a
// record, and every function that touches the chain takes one as an argument. A package-level
// variable here would be the single thing capable of sending a sandbox payment to mainnet.
package network

import "errors"

type Network string

const (
	Sandbox Network = "sandbox"
	Mainnet Network = "mainnet"
)

var ErrUnknown = errors.New("network must be sandbox or mainnet")

// All is for iterating configured networks. It is deliberately NOT a default: a caller must still
// decide which of these it has configuration for.
func All() []Network { return []Network{Sandbox, Mainnet} }

func Parse(s string) (Network, error) {
	switch Network(s) {
	case Sandbox:
		return Sandbox, nil
	case Mainnet:
		return Mainnet, nil
	}
	return "", ErrUnknown
}

func (n Network) String() string { return string(n) }

func (n Network) Valid() bool { return n == Sandbox || n == Mainnet }

// IDPrefix is the segment that appears inside every identifier belonging to this network:
// agt_test_… and agt_live_…
//
// It is what makes I8 structural rather than aspirational. MongoDB refuses any update that
// modifies _id, so a record's network is immutable at the storage engine — stronger than a
// trigger — and a validator pins the redundant `network` field to this prefix so the two cannot
// drift apart. It is also visible to the naked eye in a URL, a log line and a support ticket.
func (n Network) IDPrefix() string {
	if n == Sandbox {
		return "test"
	}
	return "live"
}

// KeyPrefix is the API-key prefix an agent on this network carries. S0 checks it first, before
// any other rule, because a network mismatch means every subsequent comparison would be made
// against the wrong world.
func (n Network) KeyPrefix() string {
	if n == Sandbox {
		return "lk_test_"
	}
	return "lk_live_"
}

// FromIDPrefix is the inverse of IDPrefix.
func FromIDPrefix(p string) (Network, error) {
	switch p {
	case "test":
		return Sandbox, nil
	case "live":
		return Mainnet, nil
	}
	return "", ErrUnknown
}

// FromKeyPrefix reads the network an API key claims. It says nothing about whether the key is
// valid — only which world it says it belongs to, so S0 can compare that against the agent.
func FromKeyPrefix(key string) (Network, error) {
	switch {
	case len(key) > 8 && key[:8] == "lk_test_":
		return Sandbox, nil
	case len(key) > 8 && key[:8] == "lk_live_":
		return Mainnet, nil
	}
	return "", ErrUnknown
}

// IsSandbox exists so callers read as prose at the two places it matters: the faucet, and the
// demo-402 endpoint, both of which are nonsensical on mainnet rather than merely forbidden.
func (n Network) IsSandbox() bool { return n == Sandbox }
