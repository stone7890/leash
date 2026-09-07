package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stone7890/leash/internal/domain/ids"
	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/domain/state"
	"github.com/stone7890/leash/internal/domain/tier"
)

// The golden vectors.
//
// A case states only what it changes. Everything else comes from a baseline that passes all eight
// rules, so a vector reads as "this one thing is different, and here is what happens". The
// defaults are applied by unmarshalling the case's JSON OVER a populated struct, which is why a
// case can override `allowance.state` without restating the cap.

const vectorDir = "../../../../contracts/policy-vectors"

type vectorFile struct {
	Rule string `json:"rule"`
	Code string `json:"code"`
	// Cases stay raw. The whole mechanism depends on knowing which fields a case actually
	// specified — decoding into a struct first would fill in zero values and lose exactly that.
	Cases []json.RawMessage `json:"cases"`
}

type vectorCase struct {
	Name     string          `json:"name"`
	Now      string          `json:"now"`
	Snapshot json.RawMessage `json:"snapshot"`
	Request  json.RawMessage `json:"request"`
	Expect   jsonExpect      `json:"expect"`
}

type jsonSnap struct {
	KeyNetwork   string `json:"key_network"`
	ExpectedMint string `json:"expected_mint"`
	TakenAt      string `json:"taken_at"`
	Agent        struct {
		Network string `json:"network"`
		Killed  bool   `json:"killed"`
	} `json:"agent"`
	Policy struct {
		AllowHosts     []string   `json:"allow_hosts"`
		AllowPayTo     []string   `json:"allow_pay_to"`
		AllowAll       bool       `json:"allow_all"`
		PerTxMax       money.Base `json:"per_tx_max"`
		VelocityMax    money.Base `json:"velocity_max"`
		VelocityWindow int        `json:"velocity_window_s"`
	} `json:"policy"`
	Allowance struct {
		State      string     `json:"state"`
		Cap        money.Base `json:"cap"`
		Drawn      money.Base `json:"drawn"`
		Reserved   money.Base `json:"reserved"`
		ExpiryTS   string     `json:"expiry_ts"`
		ExpiryTier string     `json:"expiry_tier"`
		Velocity   []struct {
			At     string     `json:"at"`
			Amount money.Base `json:"amount"`
		} `json:"velocity"`
	} `json:"allowance"`
}

type jsonReq struct {
	Host                   string     `json:"host"`
	PayTo                  string     `json:"pay_to"`
	Amount                 money.Base `json:"amount"`
	Mint                   string     `json:"mint"`
	Scheme                 string     `json:"scheme"`
	Network                string     `json:"network"`
	RecipientAccountExists *bool      `json:"recipient_account_exists"`
}

type jsonExpect struct {
	Verdict    string `json:"verdict"`
	FailedRule string `json:"failed_rule"`
	Code       string `json:"code"`
	Tier       string `json:"tier"`
}

const (
	defaultNow    = "2026-09-04T14:00:00Z"
	defaultExpiry = "2026-09-11T14:00:00Z"
	sandboxMint   = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	payTo         = "Ex4Yb8mQjWnGqLpTdRsVzKcHaFuNxPjW9mQ2kTdRsVzK"
)

// baseline passes all eight rules. Every case is a deviation from it.
func baseline() (jsonSnap, jsonReq) {
	var s jsonSnap
	s.KeyNetwork = "sandbox"
	s.ExpectedMint = sandboxMint
	s.TakenAt = defaultNow
	s.Agent.Network = "sandbox"
	s.Agent.Killed = false
	s.Policy.AllowHosts = []string{"api.exa.ai", "api.helius.dev"}
	s.Policy.AllowAll = false
	s.Policy.PerTxMax = 250_000      // $0.25
	s.Policy.VelocityMax = 1_000_000 // $1.00
	s.Policy.VelocityWindow = 600
	s.Allowance.State = "active"
	s.Allowance.Cap = 10_000_000 // $10
	s.Allowance.Drawn = 0
	s.Allowance.Reserved = 0
	s.Allowance.ExpiryTS = defaultExpiry
	s.Allowance.ExpiryTier = "signer"

	var r jsonReq
	r.Host = "api.exa.ai"
	r.PayTo = payTo
	r.Amount = 10_000 // $0.01
	r.Mint = sandboxMint
	r.Scheme = "exact"
	r.Network = "sandbox"
	return s, r
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	if s == "" {
		return time.Time{}
	}
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad timestamp %q: %v", s, err)
	}
	return v
}

func build(t *testing.T, js jsonSnap, jr jsonReq) (Snapshot, Request) {
	t.Helper()
	agentID, _ := ids.NewOn(ids.KindAgent, network.Network(js.Agent.Network))
	alwID, _ := ids.NewOn(ids.KindAllowance, network.Network(js.Agent.Network))

	var vel []VelocityEntry
	for _, v := range js.Allowance.Velocity {
		vel = append(vel, VelocityEntry{At: mustTime(t, v.At), Amount: v.Amount})
	}

	s := Snapshot{
		Agent: AgentView{
			ID: agentID, Network: network.Network(js.Agent.Network), Killed: js.Agent.Killed,
		},
		Policy: PolicyView{
			AllowHosts:     js.Policy.AllowHosts,
			AllowPayTo:     js.Policy.AllowPayTo,
			AllowAll:       js.Policy.AllowAll,
			PerTxMax:       js.Policy.PerTxMax,
			VelocityMax:    js.Policy.VelocityMax,
			VelocityWindow: time.Duration(js.Policy.VelocityWindow) * time.Second,
		},
		Allowance: AllowanceView{
			ID: alwID, State: state.Allowance(js.Allowance.State),
			Cap: js.Allowance.Cap, Drawn: js.Allowance.Drawn, Reserved: js.Allowance.Reserved,
			ExpiryTS:   mustTime(t, js.Allowance.ExpiryTS),
			ExpiryTier: tier.Tier(js.Allowance.ExpiryTier),
			Mint:       sandboxMint, LastReadSlot: 356442108, Velocity: vel,
		},
		KeyNetwork:   network.Network(js.KeyNetwork),
		ExpectedMint: js.ExpectedMint,
		TakenAt:      mustTime(t, js.TakenAt),
	}

	exists := true
	if jr.RecipientAccountExists != nil {
		exists = *jr.RecipientAccountExists
	}
	r := Request{
		Host: jr.Host, PayTo: jr.PayTo, Amount: jr.Amount, Mint: jr.Mint,
		Scheme: jr.Scheme, Network: network.Network(jr.Network),
		RecipientAccountExists: exists,
	}
	return s, r
}

func TestGoldenVectors(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(vectorDir, "S*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no vector files found in %s: %v", vectorDir, err)
	}

	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		var vf vectorFile
		if err := json.Unmarshal(b, &vf); err != nil {
			t.Fatalf("parsing %s: %v", f, err)
		}

		t.Run(vf.Rule, func(t *testing.T) {
			for _, rawCase := range vf.Cases {
				var c vectorCase
				if err := json.Unmarshal(rawCase, &c); err != nil {
					t.Fatalf("parsing a case in %s: %v", f, err)
				}
				t.Run(c.Name, func(t *testing.T) {
					// Decode each case OVER a fresh baseline. encoding/json leaves absent fields
					// untouched, so a case states only what it changes.
					js, jr := baseline()
					if len(c.Snapshot) > 0 {
						if err := json.Unmarshal(c.Snapshot, &js); err != nil {
							t.Fatalf("snapshot: %v", err)
						}
					}
					if len(c.Request) > 0 {
						if err := json.Unmarshal(c.Request, &jr); err != nil {
							t.Fatalf("request: %v", err)
						}
					}

					now := defaultNow
					if c.Now != "" {
						now = c.Now
					}
					s, r := build(t, js, jr)
					got := Evaluate(s, r, mustTime(t, now))

					if string(got.Verdict) != c.Expect.Verdict {
						t.Fatalf("verdict = %s, want %s (failed rule %v)",
							got.Verdict, c.Expect.Verdict, got.FailedRule)
					}
					if c.Expect.Verdict == "blocked" {
						if got.FailedRule.String() != c.Expect.FailedRule {
							t.Errorf("failed rule = %s, want %s",
								got.FailedRule, c.Expect.FailedRule)
						}
						if got.Fault.Code() != c.Expect.Code {
							t.Errorf("code = %s, want %s", got.Fault.Code(), c.Expect.Code)
						}
						if c.Expect.Tier != "" {
							ft := got.Checks[int(got.FailedRule)].Tier
							if string(ft) != c.Expect.Tier {
								t.Errorf("tier = %s, want %s", ft, c.Expect.Tier)
							}
						}
					}
					// Every rule always has a line, so the interface can render "N of N" from
					// the array rather than a literal.
					if len(got.Checks) != len(Rules) {
						t.Errorf("checks = %d, want %d", len(got.Checks), len(Rules))
					}
				})
			}
		})
	}
}

// A vector nobody wrote is a coverage gap nobody can see.
func TestEveryRuleHasAtLeastThreeVectorsAndOneBoundary(t *testing.T) {
	for _, rule := range Rules {
		f := filepath.Join(vectorDir, rule.String()+".json")
		b, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("%s has no vector file at %s", rule, f)
			continue
		}
		var vf vectorFile
		if err := json.Unmarshal(b, &vf); err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if len(vf.Cases) < 3 {
			t.Errorf("%s has %d cases, want at least three (pass, fail, boundary)",
				rule, len(vf.Cases))
		}
		var pass, fail, boundary bool
		for _, rawCase := range vf.Cases {
			var c vectorCase
			if err := json.Unmarshal(rawCase, &c); err != nil {
				t.Errorf("%s: %v", f, err)
				continue
			}
			switch c.Expect.Verdict {
			case "allowed":
				pass = true
			case "blocked":
				fail = true
			}
			if containsFold(c.Name, "boundary") {
				boundary = true
			}
		}
		if !pass {
			t.Errorf("%s has no passing case", rule)
		}
		if !fail {
			t.Errorf("%s has no blocking case", rule)
		}
		if !boundary {
			t.Errorf("%s has no case named as a boundary", rule)
		}
		if vf.Code != rule.Code() {
			t.Errorf("%s: vector file says code %q, the engine says %q", rule, vf.Code, rule.Code())
		}
	}
}

func containsFold(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 32
		}
		if 'A' <= y && y <= 'Z' {
			y += 32
		}
		if x != y {
			return false
		}
	}
	return true
}
