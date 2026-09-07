package challenge

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Conformance against pay-kit's own cross-SDK vectors.
//
// These are the same files every other implementation of x402 is tested against. Passing them is
// what makes the claim "an agent using `pay` can pay through Leash" checkable rather than hopeful
// — and if the protocol moves and we do not, one of these fails instead of a customer's payment.

const vectorDir = "../../../../contracts/x402"

type vector struct {
	ID          string         `json:"id"`
	Mode        string         `json:"mode"`
	Description string         `json:"description"`
	Input       map[string]any `json:"input"`
	Expect      map[string]any `json:"expect"`
}

func load(t *testing.T, name string) []vector {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(vectorDir, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	var vs []vector
	if err := json.Unmarshal(b, &vs); err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	if len(vs) == 0 {
		t.Fatalf("%s has no vectors", name)
	}
	return vs
}

// The network mapping is where a wrong answer is worst: a challenge for one cluster settled
// against another is the isolation failure invariant I8 exists to prevent, arriving from outside.
func TestNetworkMappingMatchesPayKit(t *testing.T) {
	for _, c := range []struct {
		wire, cluster string
	}{
		{"solana", "mainnet-beta"},
		{"solana-devnet", "devnet"},
		{"solana-testnet", "testnet"},
		{CAIP2Mainnet, "mainnet-beta"},
		{CAIP2Devnet, "devnet"},
		{CAIP2Testnet, "testnet"},
	} {
		got, ok := ClusterOf(c.wire)
		if !ok || got != c.cluster {
			t.Errorf("ClusterOf(%q) = %q, %v — want %q", c.wire, got, ok, c.cluster)
		}
	}
	// An unknown network is refused, not guessed at.
	if _, ok := ClusterOf("ethereum"); ok {
		t.Error("an unknown network was accepted")
	}
	if _, ok := ClusterOf(""); ok {
		t.Error("an empty network was accepted")
	}
}

// v1 producers emit plain slugs; v2 producers emit CAIP-2. Emitting the wrong one for the version
// the peer declared is how a payment is refused for a reason that looks like a signature problem.
func TestWireNetworkMatchesTheVersion(t *testing.T) {
	for _, c := range []struct {
		v    Version
		net  string
		want string
	}{
		{V1, "sandbox", SlugDevnet},
		{V1, "mainnet", SlugMainnet},
		{V2, "sandbox", CAIP2Devnet},
		{V2, "mainnet", CAIP2Mainnet},
	} {
		if got := WireNetwork(netOf(c.net), c.v); got != c.want {
			t.Errorf("WireNetwork(%s, v%d) = %q, want %q", c.net, c.v, got, c.want)
		}
	}
}

// The build vectors carry real offers, produced by another implementation. Ours must read every
// field of them, under both versions' names.
func TestOffersFromPayKitVectorsParse(t *testing.T) {
	for _, file := range []string{"x402-v1-build.json", "x402-build.json"} {
		for _, v := range load(t, file) {
			offer, ok := v.Input["x402Offer"].(map[string]any)
			if !ok {
				continue
			}
			t.Run(v.ID, func(t *testing.T) {
				// A v2 vector omits x402Version from its input, because v2 is the default
				// producer and the version is implied. Defaulting to v2 is what a real reader
				// must do rather than refusing.
				version := any(int(V2))
				if raw, ok := v.Input["x402Version"]; ok {
					version = raw
				}
				body, _ := json.Marshal(map[string]any{
					"x402Version": version,
					"accepts":     []any{offer},
				})

				req, err := ParseRequirements(body)
				if err != nil {
					t.Fatalf("parsing a pay-kit offer failed: %v", err)
				}
				ch, err := Select(req, req.Accepts[0], "localhost:3402")
				if err != nil {
					t.Fatalf("selecting a pay-kit offer failed: %v", err)
				}

				if ch.Scheme != SchemeExact {
					t.Errorf("scheme = %q", ch.Scheme)
				}
				if ch.PayTo == "" || ch.Asset == "" {
					t.Errorf("payTo or asset was lost: %+v", ch)
				}

				// Base units. The vector says "1000"; reading it as a decimal would give a number
				// a million times smaller, and underpay every invoice.
				want, _ := offer["amount"].(string)
				if want == "" {
					want, _ = offer["maxAmountRequired"].(string)
				}
				if ch.Amount.BaseUnits() != want {
					t.Errorf("amount = %s, want %s", ch.Amount.BaseUnits(), want)
				}

				// extra.feePayer, WHEN THE OFFER CARRIES ONE, must survive. Without it the agent
				// would be expected to pay its own fees, which pay-kit's verifier refuses outright
				// — an offer that omits it is simply an offer with no sponsor.
				if extra, ok := offer["extra"].(map[string]any); ok {
					if wantFP, ok := extra["feePayer"].(string); ok && wantFP != "" {
						got, present := ch.FeePayer()
						if !present || got != wantFP {
							t.Errorf("extra.feePayer = %q, want %q", got, wantFP)
						}
					}
				}
			})
		}
	}
}

// The verify vectors carry real base64 payment headers produced by another implementation, and
// their own verdicts. A `reject` vector must be refused for the reason it names.
func TestPaymentEnvelopesFromPayKitVectorsParse(t *testing.T) {
	for _, v := range load(t, "x402-v1-verify.json") {
		header, ok := v.Input["x402PaymentHeader"].(string)
		if !ok || header == "" {
			continue
		}
		outcome, _ := v.Expect["outcome"].(string)

		t.Run(v.ID, func(t *testing.T) {
			// Standard PADDED base64, not base64url. Getting this wrong rejects every real payment.
			raw, err := base64.StdEncoding.DecodeString(header)
			if err != nil {
				t.Fatalf("the header is not standard base64: %v", err)
			}
			var env PaymentEnvelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("parsing the envelope: %v", err)
			}

			// The two gates a server applies before it settles anything: a version it understands,
			// and a network that matches the route.
			accepted := VersionSupported(env.Version)
			serverNet, _ := v.Input["x402ServerNetwork"].(string)
			if accepted && serverNet != "" {
				cluster, ok := ClusterOf(env.Network)
				accepted = ok && cluster == serverNet
			}

			switch outcome {
			case "accept":
				if !accepted {
					t.Errorf("a valid credential was refused (version %d, network %q against %q)",
						env.Version, env.Network, serverNet)
				}
				if env.Scheme != SchemeExact {
					t.Errorf("v1 carries scheme beside payload; got %q", env.Scheme)
				}
				if env.Payload.Transaction == "" {
					t.Error("payload.transaction is empty")
				}
				// v1 has no `accepted` object. Requiring one would reject every v1 payment.
				if env.Accepted != nil {
					t.Error("v1 must not carry an accepted object")
				}
			case "reject":
				if accepted {
					t.Errorf("a credential pay-kit rejects was accepted: %s", v.Description)
				}
			}
		})
	}
}

// An unknown version is refused rather than guessed at — pay-kit's server does the same, and says
// why: settling a payment whose wire contract you do not understand is worse than refusing it.
func TestAnUnknownVersionIsRefused(t *testing.T) {
	for _, body := range []string{
		`{"x402Version":3,"accepts":[{"scheme":"exact","network":"solana-devnet","amount":"1","asset":"a","payTo":"b"}]}`,
		`{"accepts":[{"scheme":"exact"}]}`,
		`{"x402Version":0,"accepts":[]}`,
	} {
		if _, err := ParseRequirements([]byte(body)); err == nil {
			t.Errorf("accepted a challenge we cannot canonicalise: %s", body)
		}
	}
}

func netOf(s string) networkAlias { return networkAlias(s) }
