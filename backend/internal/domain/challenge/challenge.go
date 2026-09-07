// Package challenge parses an x402 payment challenge and hashes it.
//
// The wire format is not ours — see x402.go and contracts/x402/. What IS ours is the hash: it is
// the idempotency key for the whole outward path (invariant I7), so that a retry carrying the same
// challenge produces the same signature and never a second one.
//
// Two consequences follow.
//
// First, canonicalisation must be exact and stable. If the same challenge can hash two ways, one
// API call gets two signatures and the customer pays twice.
//
// Second, an unrecognised version is REFUSED rather than canonicalised by guesswork. pay-kit's own
// server does the same and says why: a server must not silently accept an unknown version, because
// it would settle a payment whose wire contract it does not understand.
package challenge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/stone7890/leash/internal/domain/fault"
	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
)

// Known lists the versions this build can parse and hash.
func Known() []Version { return []Version{V1, V2} }

func known(v Version) bool {
	for _, k := range Known() {
		if k == v {
			return true
		}
	}
	return false
}

// Challenge is one offer, validated, and the context needed to act on it.
type Challenge struct {
	Version Version
	Scheme  string
	Network network.Network
	PayTo   string
	Amount  money.Base
	Asset   string
	Offer   Offer

	// Host is the endpoint that issued the challenge. It is NOT part of the hash — the same
	// challenge is the same payment however the server was addressed — but it is what rule S2
	// checks and what the one-tap recovery button adds.
	Host string

	// Resource is the thing being bought. It IS part of the hash: two calls to different resources
	// are two payments even when they cost the same.
	Resource string
}

// FeePayer is the facilitator that will pay the network fee, if the offer names one.
func (c Challenge) FeePayer() (string, bool) { return c.Offer.FeePayer() }

// ParseRequirements reads a 402 body or challenge header.
//
// It accepts both wire shapes because the field names differ between versions and a peer sends
// whichever it produces: v1 says `maxAmountRequired`, `currency` and `recipient`; v2 says
// `amount`, `asset` and `payTo`. pay-kit's own deserialiser accepts both, and refusing one would
// mean refusing perfectly valid traffic from half the implementations in the field.
func ParseRequirements(raw []byte) (Requirements, error) {
	var wire struct {
		Version json.Number `json:"x402Version"`
		Error   string      `json:"error"`
		Accepts []struct {
			Scheme string `json:"scheme"`
			Net    string `json:"network"`

			Amount            string `json:"amount"`
			MaxAmountRequired string `json:"maxAmountRequired"`

			Asset    string `json:"asset"`
			Currency string `json:"currency"`

			PayTo     string `json:"payTo"`
			Recipient string `json:"recipient"`

			Resource          string         `json:"resource"`
			Description       string         `json:"description"`
			MimeType          string         `json:"mimeType"`
			MaxTimeoutSeconds int            `json:"maxTimeoutSeconds"`
			MaxAge            int            `json:"maxAge"`
			Extra             map[string]any `json:"extra"`
		} `json:"accepts"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Requirements{}, fault.MalformedRequest.Wrapping(err).
			WithMessage("the challenge is not valid JSON")
	}

	v, err := strconv.Atoi(wire.Version.String())
	if err != nil || v == 0 {
		return Requirements{}, fault.UnsupportedChallengeVersion.
			WithMessage("the challenge does not state an x402 version")
	}
	if !known(Version(v)) {
		return Requirements{}, fault.UnsupportedChallengeVersion.
			WithMessage("x402 version " + wire.Version.String() + " is not recognised by this build").
			WithDetails(map[string]any{"known": []int{int(V1), int(V2)}})
	}

	out := Requirements{Version: Version(v), Error: wire.Error}
	for _, a := range wire.Accepts {
		o := Offer{
			Scheme:  strings.TrimSpace(a.Scheme),
			Network: strings.TrimSpace(a.Net),
			// The fallback chain pay-kit uses, in the order it uses it.
			Amount:            firstNonEmpty(a.Amount, a.MaxAmountRequired),
			Asset:             firstNonEmpty(a.Asset, a.Currency),
			PayTo:             firstNonEmpty(a.PayTo, a.Recipient),
			Resource:          a.Resource,
			Description:       a.Description,
			MimeType:          a.MimeType,
			MaxTimeoutSeconds: firstNonZero(a.MaxTimeoutSeconds, a.MaxAge),
			Extra:             a.Extra,
		}
		out.Accepts = append(out.Accepts, o)
	}
	if len(out.Accepts) == 0 {
		return Requirements{}, fault.MalformedRequest.
			WithMessage("the challenge carried no payment requirements")
	}
	return out, nil
}

// Select validates one offer and turns it into something we are willing to sign.
//
// Strict on purpose. Every field it needs is required, and an offer that is missing one stops here
// rather than reaching the hash — a challenge we cannot canonicalise is one we must not sign.
func Select(req Requirements, o Offer, host string) (Challenge, error) {
	if o.Scheme != SchemeExact {
		// Rule S6's first half, refused before anything else is examined.
		return Challenge{}, fault.UnsupportedTerms.
			WithMessage("only the `exact` scheme is supported, and this offer asked for `" +
				o.Scheme + "`")
	}

	n, ok := NetworkOf(o.Network)
	if !ok {
		return Challenge{}, fault.InvalidNetwork.
			WithMessage("the offer names a network this build does not know: " + o.Network)
	}

	// Base units, as an integer string. The protocol says "1000"; a decimal here would be a
	// different number by a factor of a million, silently.
	amt, err := money.ParseBaseUnits(o.Amount)
	if err != nil {
		return Challenge{}, fault.InvalidAmount.Wrapping(err).
			WithMessage("the offer's amount is not an integer count of base units")
	}

	c := Challenge{
		Version:  req.Version,
		Scheme:   o.Scheme,
		Network:  n,
		PayTo:    strings.TrimSpace(o.PayTo),
		Amount:   amt,
		Asset:    strings.TrimSpace(o.Asset),
		Offer:    o,
		Host:     strings.ToLower(strings.TrimSpace(host)),
		Resource: strings.TrimSpace(o.Resource),
	}
	for name, val := range map[string]string{"payTo": c.PayTo, "asset": c.Asset} {
		if val == "" {
			return Challenge{}, fault.MalformedRequest.WithField(name).
				WithMessage("the offer is missing " + name)
		}
	}
	if c.Host == "" {
		return Challenge{}, fault.InvalidHost.WithMessage("the challenge has no host")
	}
	return c, nil
}

// Canonical renders the challenge as the exact bytes that are hashed.
//
// A fixed field order, one field per line, `key=value`, with a version tag first so a future
// format cannot collide with this one. It is legible in a log, which matters when reconciling a
// disputed payment.
//
// FIELD ORDER IS FROZEN. Reordering it changes every hash and orphans every idempotency record
// already written — the next retry of an in-flight payment would be signed a second time.
//
// WHAT IS HASHED, AND WHY IT DIFFERS FROM THE PROTOCOL.
//
// x402 has no nonce. Its replay protection is the transaction itself: a blockhash expires, and a
// signature can land only once. That is sufficient for the chain and insufficient for us, because
// invariant I7 is about not SIGNING twice, which happens before anything reaches the chain.
//
// So the hash covers the offer's identifying fields plus the `resource` — the thing being bought.
// Two calls to different resources are two payments even at the same price. Two calls to the SAME
// resource at the same price within one claim window are treated as one payment, which is the
// deliberate reading of "the same challenge": an agent that retries after a timeout gets its
// original signature back rather than a second charge. This resolves the specification's open
// question about canonicalisation, and it is registered in docs/16-deck-conformance.md.
func (c Challenge) Canonical() string {
	var b strings.Builder
	b.WriteString("x402=")
	b.WriteString(strconv.Itoa(int(c.Version)))
	b.WriteString("\nscheme=")
	b.WriteString(c.Scheme)
	b.WriteString("\nnetwork=")
	// The cluster, not the wire form: the same offer sent as v1 `solana-devnet` and as v2 CAIP-2
	// is the same payment, and must not hash two ways.
	cluster, _ := ClusterOf(c.Offer.Network)
	b.WriteString(string(cluster))
	b.WriteString("\npay_to=")
	b.WriteString(c.PayTo)
	b.WriteString("\nasset=")
	b.WriteString(c.Asset)
	b.WriteString("\namount=")
	// Base units, exactly as the protocol carries them.
	b.WriteString(strconv.FormatInt(int64(c.Amount), 10))
	b.WriteString("\nresource=")
	b.WriteString(c.Resource)
	b.WriteString("\n")
	return b.String()
}

// Hash identifies the OFFER: what is being bought, from whom, for how much.
//
// It is NOT sufficient as an idempotency key on its own, and understanding why matters. An x402
// challenge carries no nonce — two calls to the same resource at the same price produce byte-
// identical challenges, because the protocol's replay protection lives in the transaction's memo
// rather than in the offer. So hashing the offer alone would make every purchase of the same
// resource collide, and the second one would be handed the first one's signature.
//
// The claim key is ClaimKey below. This hash is recorded alongside it, because "which offer was
// this" is exactly the question an audit asks.
func (c Challenge) Hash() string {
	sum := sha256.Sum256([]byte(c.Canonical()))
	return hex.EncodeToString(sum[:])
}

// ClaimKey is what invariant I7 actually keys on: this agent, this attempt, this offer.
//
// The specification assumed the challenge carried a nonce and made `challenge_hash` unique on its
// own. Real x402 has no nonce, so the attempt has to be identified by the caller — which is what
// the agent's idempotency key is for, and it is the same discipline every creating POST in this
// API already follows.
//
// The three parts each do something:
//
//   - the AGENT, so two agents cannot collide on a shared key;
//   - the ATTEMPT, so a retry replays and a new purchase does not;
//   - the OFFER, so one key cannot be reused to authorise a different payment — an agent that
//     retries with the same key but a changed amount gets a different claim, and the change is
//     visible rather than silently signed.
func ClaimKey(agentID, attempt string, c Challenge) string {
	sum := sha256.Sum256([]byte(
		"agent=" + agentID + "\nattempt=" + attempt + "\noffer=" + c.Hash() + "\n"))
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
