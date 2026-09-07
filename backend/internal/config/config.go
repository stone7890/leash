// Package config reads the environment once, at start-up.
//
// Three rules, from docs/14-deployment.md:
//
//   - Secrets are never defaulted. A default secret is a secret in production.
//   - Half-configured is a boot failure, naming what is missing. A subsystem that is partly set up
//     fails now, in a sentence, rather than at three in the morning in a stack trace.
//   - There is deliberately no RPC_URL. Invariant I8 says the endpoint is chosen per record, and a
//     single global would be the one variable capable of sending a sandbox payment to mainnet.
//
// Every missing variable is named AT ONCE rather than the first one found. Fixing one variable per
// restart is a bad afternoon.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stone7890/leash/internal/domain/network"
)

// Mint is a token mint and the program that owns it.
//
// USDC is classic SPL, not Token-2022, and using the wrong program produces a confusing failure
// rather than an obvious one — so the program travels with the mint rather than being assumed.
type Mint struct {
	Address   string
	ProgramID string
}

// RPC is one network's endpoints. The fallback is what the indexer moves to when the primary
// stops answering; the degraded banner says so rather than hiding it.
type RPC struct {
	Primary  string
	Fallback string
}

// Common is what every binary needs.
type Common struct {
	MongoURI string
	MongoDB  string
	LogLevel string
	Networks []network.Network
	RPC      map[network.Network]RPC
	Mints    map[network.Network]Mint
}

type loader struct {
	missing []string
	bad     []string
}

func (l *loader) require(name string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		l.missing = append(l.missing, name)
	}
	return v
}

func (l *loader) optional(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func (l *loader) duration(name, def string) time.Duration {
	raw := l.optional(name, def)
	d, err := time.ParseDuration(raw)
	if err != nil {
		l.bad = append(l.bad, fmt.Sprintf("%s (%q is not a duration)", name, raw))
	}
	return d
}

func (l *loader) intVal(name string, def int) int {
	raw := l.optional(name, strconv.Itoa(def))
	n, err := strconv.Atoi(raw)
	if err != nil {
		l.bad = append(l.bad, fmt.Sprintf("%s (%q is not a number)", name, raw))
	}
	return n
}

// requireFor collects a variable that is only needed because something else was enabled. The
// message says WHY it became required, which is the difference between a fixable error and a
// puzzling one.
func (l *loader) requireFor(name, because string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		l.missing = append(l.missing, fmt.Sprintf("%s (required because %s)", name, because))
	}
	return v
}

func (l *loader) err(binary string) error {
	if len(l.missing) == 0 && len(l.bad) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s cannot start.\n", binary)
	if len(l.missing) > 0 {
		b.WriteString("\n  not set:\n")
		for _, m := range l.missing {
			fmt.Fprintf(&b, "    %s\n", m)
		}
	}
	if len(l.bad) > 0 {
		b.WriteString("\n  not valid:\n")
		for _, m := range l.bad {
			fmt.Fprintf(&b, "    %s\n", m)
		}
	}
	b.WriteString("\nSee docs/14-deployment.md, or copy .env.example.")
	return fmt.Errorf("%s", b.String())
}

// loadCommon reads what every binary shares, including the per-network configuration.
//
// The half-configured rule bites here: if mainnet is in NETWORKS and RPC_MAINNET_URL is unset, the
// process refuses to start. The alternative is mainnet enabled and silently pointing somewhere
// else, which is exactly the failure I8 exists to prevent.
func loadCommon(l *loader) Common {
	c := Common{
		MongoURI: l.require("MONGO_URI"),
		MongoDB:  l.optional("MONGO_DB", "leash"),
		LogLevel: l.optional("LOG_LEVEL", "info"),
		RPC:      map[network.Network]RPC{},
		Mints:    map[network.Network]Mint{},
	}

	raw := l.optional("NETWORKS", "sandbox")
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := network.Parse(part)
		if err != nil {
			l.bad = append(l.bad, fmt.Sprintf("NETWORKS (%q is not sandbox or mainnet)", part))
			continue
		}
		c.Networks = append(c.Networks, n)
	}
	if len(c.Networks) == 0 {
		l.bad = append(l.bad, "NETWORKS (no valid network listed)")
	}

	// The sandbox's mint is created by `leashctl bootstrap`, which writes what it made to a shared
	// volume. Reading it here is what lets `make start` work on a fresh machine with no manual
	// step. It applies to the sandbox alone — a mainnet mint is configured explicitly or not at all.
	sandbox, hasSandboxState := readSandboxState()

	// Per network, and only for the networks actually enabled. There is no RPC_URL.
	for _, n := range c.Networks {
		up := strings.ToUpper(n.String())
		because := fmt.Sprintf("NETWORKS includes %s", n)
		c.RPC[n] = RPC{
			Primary:  l.requireFor("RPC_"+up+"_URL", because),
			Fallback: l.optional("RPC_"+up+"_FALLBACK_URL", ""),
		}

		mint := strings.TrimSpace(os.Getenv("USDC_MINT_" + up))
		if mint == "" && n == network.Sandbox && hasSandboxState {
			mint = sandbox.Mint
		}
		if mint == "" {
			l.missing = append(l.missing, fmt.Sprintf(
				"USDC_MINT_%s (required because %s; on the sandbox it is normally written by "+
					"`leashctl bootstrap` into $LEASH_STATE_DIR/sandbox.json)", up, because))
		}
		c.Mints[n] = Mint{
			Address:   mint,
			ProgramID: l.optional("TOKEN_PROGRAM_"+up, "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"),
		}
	}
	return c
}

// Signer is the policy signer's configuration.
type Signer struct {
	Common
	Bind string

	// KMSKeyARN is required and never defaulted. An empty one would mean an agent key encrypted
	// with nothing.
	KMSKeyARN string
	AWSRegion string

	// KMSLocal enables a development key wrapper that does not call AWS.
	//
	// It exists so the demo runs on a laptop and on a fresh VM without an AWS account. It refuses
	// to be combined with mainnet — see Load below — because a locally-wrapped key protecting real
	// money is exactly the shortcut that should not be one flag away.
	KMSLocal bool

	SnapshotTTL  time.Duration
	KeyCacheTTL  time.Duration
	BlockhashTTL time.Duration
}

func LoadSigner() (Signer, error) {
	l := &loader{}
	s := Signer{Common: loadCommon(l)}
	s.Bind = l.optional("SIGNER_BIND", ":4100")

	s.KMSLocal = l.optional("KMS_LOCAL", "false") == "true"
	if !s.KMSLocal {
		s.KMSKeyARN = l.require("KMS_KEY_ARN")
		s.AWSRegion = l.require("AWS_REGION")
	}

	// The specification's numbers. The snapshot TTL is 60s because rule S1 says the allowance may
	// be cached for at most that long; the blockhash is 30s because a Solana blockhash lasts 60–90
	// and refreshing at half leaves margin.
	s.SnapshotTTL = l.duration("SIGNER_SNAPSHOT_TTL", "60s")
	s.KeyCacheTTL = l.duration("SIGNER_KEY_TTL", "10m")
	s.BlockhashTTL = l.duration("SIGNER_BLOCKHASH_TTL", "30s")

	if err := l.err("leash-signer"); err != nil {
		return Signer{}, err
	}
	// Half-configured, of the most dangerous kind: development key wrapping, on the network where
	// the money is real.
	for _, n := range s.Networks {
		if s.KMSLocal && n == network.Mainnet {
			return Signer{}, fmt.Errorf(
				"leash-signer cannot start: KMS_LOCAL=true wraps agent keys with a development " +
					"key held on this machine, and NETWORKS includes mainnet. Set KMS_KEY_ARN " +
					"and AWS_REGION, or remove mainnet from NETWORKS")
		}
	}
	if s.SnapshotTTL > 60*time.Second {
		return Signer{}, fmt.Errorf(
			"leash-signer cannot start: SIGNER_SNAPSHOT_TTL is %s. Rule S1 permits a cache of at "+
				"most 60s — a staler view is not evidence that an allowance is live", s.SnapshotTTL)
	}
	return s, nil
}

// Indexer is the read-back worker's configuration.
type Indexer struct {
	Common
	Bind string

	AllowanceRefresh time.Duration
	PaymentSweep     time.Duration
	AlertEval        time.Duration
	FaucetGuard      time.Duration

	// How long a payment with no trace on chain stays `unknown` before the sweep calls it failed
	// and gives its reservation back. Per network, like every other setting whose right answer
	// differs between play money and real money.
	AbandonedAfter map[network.Network]time.Duration

	AlertsEnabled bool
	TelegramToken string

	FaucetPerWalletPerHour int
	DemoEndpointURL        string
}

func LoadIndexer() (Indexer, error) {
	l := &loader{}
	i := Indexer{Common: loadCommon(l)}
	i.Bind = l.optional("INDEXER_BIND", ":4300")

	// The specification's cadences. alert-eval is 30s because the interface promises alerts within
	// thirty seconds of the event they describe — the cadence is a contract, not a preference.
	i.AllowanceRefresh = l.duration("JOB_ALLOWANCE_REFRESH", "60s")
	i.PaymentSweep = l.duration("JOB_PAYMENT_SWEEP", "5m")
	i.AlertEval = l.duration("JOB_ALERT_EVAL", "30s")
	i.FaucetGuard = l.duration("JOB_FAUCET_GUARD", "1h")

	i.AlertsEnabled = l.optional("ALERTS_ENABLED", "false") == "true"
	if i.AlertsEnabled {
		i.TelegramToken = l.requireFor("TELEGRAM_BOT_TOKEN", "ALERTS_ENABLED is true")
	}

	// A day on mainnet, ten minutes on the sandbox. The asymmetry is the point: on mainnet a
	// premature `failed` releases budget for money that may still move, which is how one challenge
	// gets paid twice; on the sandbox the same caution leaves a demo holding a cent until tomorrow
	// over a transaction nobody will ever submit.
	//
	// Two variables rather than one, because a single global spanning both networks is the shape
	// this codebase refuses everywhere else — see "There is no RPC_URL".
	i.AbandonedAfter = map[network.Network]time.Duration{
		network.Sandbox: l.duration("PAYMENT_ABANDONED_AFTER_SANDBOX", "10m"),
		network.Mainnet: l.duration("PAYMENT_ABANDONED_AFTER_MAINNET", "24h"),
	}

	i.FaucetPerWalletPerHour = l.intVal("FAUCET_PER_WALLET_PER_HOUR", 1)
	i.DemoEndpointURL = l.optional("DEMO_ENDPOINT_URL", "http://localhost:4200")

	if err := l.err("leash-indexer"); err != nil {
		return Indexer{}, err
	}
	// The sweep only looks at payments older than 90 seconds, so anything near that would call a
	// payment abandoned at the first glance it ever gets — before a healthy transaction has had
	// time to confirm. Two minutes is the floor at which the setting still means what it says.
	for n, d := range i.AbandonedAfter {
		if d < 2*time.Minute {
			return Indexer{}, fmt.Errorf(
				"leash-indexer cannot start: the %s abandon window is %s. The sweep does not look "+
					"at a payment until it is 90s old, so anything under 2m calls a payment "+
					"abandoned at its first check", n, d)
		}
	}
	if i.AlertEval > 30*time.Second {
		return Indexer{}, fmt.Errorf(
			"leash-indexer cannot start: JOB_ALERT_EVAL is %s, but the interface promises alerts "+
				"within 30s of the event they describe", i.AlertEval)
	}
	return i, nil
}

// Demo402 is the sample x402 endpoint's configuration.
type Demo402 struct {
	Common
	Bind string
	// PriceBase is $0.01 in micro-USDC, from the specification.
	PriceBase int64
	PayTo     string
}

func LoadDemo402() (Demo402, error) {
	l := &loader{}
	d := Demo402{Common: loadCommon(l)}
	d.Bind = l.optional("DEMO402_BIND", ":4200")
	d.PriceBase = int64(l.intVal("DEMO402_PRICE_BASE", 10_000))
	d.PayTo = strings.TrimSpace(os.Getenv("DEMO402_PAY_TO"))
	if d.PayTo == "" {
		if s, ok := readSandboxState(); ok {
			d.PayTo = s.Demo402PayTo
		}
	}
	if d.PayTo == "" {
		l.missing = append(l.missing, "DEMO402_PAY_TO (normally written by `leashctl bootstrap`)")
	}

	if err := l.err("leash-demo402"); err != nil {
		return Demo402{}, err
	}
	// The sample endpoint exists for onboarding step 5, on the sandbox. It must never take
	// mainnet traffic, and refusing at boot is better than refusing per request.
	for _, n := range d.Networks {
		if n == network.Mainnet {
			return Demo402{}, fmt.Errorf(
				"leash-demo402 cannot start: NETWORKS includes mainnet. The sample endpoint is " +
					"a sandbox fixture and must never accept real payments")
		}
	}
	return d, nil
}
