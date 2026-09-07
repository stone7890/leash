package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gagliardetto/solana-go"
	ata "github.com/gagliardetto/solana-go/programs/associated-token-account"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/programs/token"
	solrpc "github.com/gagliardetto/solana-go/rpc"
	"github.com/gagliardetto/solana-go/rpc/jsonrpc"
	"github.com/stone7890/leash/internal/kms"
	"github.com/stone7890/leash/internal/migrate"
	"github.com/stone7890/leash/internal/store"
)

// State is what bootstrap leaves behind for the services to read.
//
// The sandbox has no pre-existing USDC, so one is created here — and every service needs to agree
// on which mint that is. Writing it to a shared volume means they cannot disagree, and a fresh VM
// needs no manual step.
type State struct {
	Network       string    `json:"network"`
	Mint          string    `json:"usdc_mint"`
	MintAuthority string    `json:"mint_authority"`
	Demo402PayTo  string    `json:"demo402_pay_to"`
	FaucetWallet  string    `json:"faucet_wallet"`
	Facilitator   string    `json:"facilitator"`
	CreatedAt     time.Time `json:"created_at"`
}

const (
	stateFile    = "sandbox.json"
	keyMint      = "mint-authority.json"
	keyFaucet    = "faucet.json"
	keyFacilit   = "facilitator.json"
	keyRecipient = "recipient.json"
	usdcDecimal  = 6
)

// cmdBootstrap prepares a brand-new sandbox: a USDC mint, a funded faucet, and the demo endpoint's
// recipient account. It is idempotent — running it twice reuses what it made — because compose
// re-runs it on every `make start`.
func cmdBootstrap(ctx context.Context, _ []string) error {
	dir := env("LEASH_STATE_DIR", "/state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("state directory: %w", err)
	}

	// The database first, so a failure there is not discovered after minting tokens.
	st, err := open(ctx)
	if err != nil {
		return err
	}
	defer st.Close(context.Background())
	c, cancel := withTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := migrate.Apply(c, st.Migrator()); err != nil {
		return err
	}
	fmt.Printf("schema is at version %d\n", migrate.All()[len(migrate.All())-1].Version)

	// The signer mounts the state volume READ-ONLY, which is the property we want: a process
	// holding key material should not be able to write to shared storage. So the local wrapping
	// key is created here, once, rather than lazily by the signer on first use.
	if _, err := kms.NewLocal(dir); err != nil {
		return fmt.Errorf("preparing the local key wrapper: %w", err)
	}

	rpcURL := env("RPC_SANDBOX_URL", "http://localhost:8899")
	client := solrpc.New(rpcURL)
	if _, err := client.GetHealth(c); err != nil {
		return fmt.Errorf("the sandbox validator at %s is not answering: %w", rpcURL, err)
	}

	// Recorded state is only worth reusing if the CHAIN still agrees with it.
	//
	// A validator that lost its ledger while the state volume survived leaves a recorded mint that
	// does not exist, and everything downstream fails with an error naming an account rather than
	// the cause. Checking is the same read-back discipline the rest of the system follows: trust
	// what the chain says, not what we wrote down.
	if existing, err := readState(dir); err == nil {
		if live, err := mintExists(c, client, existing.Mint); err == nil && live {
			fmt.Printf("sandbox already prepared\n  mint  %s\n  payTo %s\n",
				existing.Mint, existing.Demo402PayTo)
			return nil
		}
		fmt.Printf("the recorded mint %s is not on this chain — the ledger was reset.\n"+
			"Preparing a fresh sandbox.\n", existing.Mint)
	}

	authority, err := loadOrCreateKey(filepath.Join(dir, keyMint))
	if err != nil {
		return err
	}
	faucet, err := loadOrCreateKey(filepath.Join(dir, keyFaucet))
	if err != nil {
		return err
	}

	// The sample endpoint's facilitator. It sponsors the network fee on every payment made to that
	// endpoint, which is why agents need no SOL of their own — x402 refuses a transaction whose fee
	// payer is also the transfer authority. It is also the one key that drains with use.
	facilitator, err := loadOrCreateKey(filepath.Join(dir, keyFacilit))
	if err != nil {
		return err
	}

	fmt.Println("funding the keys this sandbox needs")
	// Modest amounts: a local validator would grant any of these, and a public faucet caps at
	// around two SOL. Each covers rent and fees for what that key actually does, and nothing more.
	//
	// The faucet key is NOT here. It is created and recorded, because sandbox.json publishes it,
	// but nothing ever signs with it — the in-app faucet mints with the mint authority. Funding it
	// would spend a third of a rate-limited devnet allowance on an address that never pays for
	// anything.
	want, err := fundingTarget()
	if err != nil {
		return err
	}
	if err := fundSandbox(c, client, authority, []funding{
		{"sample endpoint's facilitator", facilitator.PublicKey(), want},
	}, want); err != nil {
		return err
	}

	fmt.Println("creating the sandbox USDC mint")
	mint := solana.NewWallet()
	if err := createMint(c, client, authority, mint); err != nil {
		return err
	}

	// In x402, `payTo` is a WALLET address and the money goes to its associated token account —
	// the client derives ATA(payTo, mint, tokenProgram) and transfers there. Publishing a bare
	// token account as payTo makes every client derive an address that does not exist, and the
	// transfer fails with InvalidAccountData rather than anything that names the cause.
	//
	// So the recipient is a wallet, and its ATA is created here so the first payment does not have
	// to.
	recipient, err := loadOrCreateKey(filepath.Join(dir, keyRecipient))
	if err != nil {
		return err
	}
	fmt.Println("creating the demo endpoint's recipient token account")
	if err := createRecipientATA(c, client, authority, recipient.PublicKey(), mint.PublicKey()); err != nil {
		return err
	}
	payTo := recipient.PublicKey()

	s := State{
		Network: "sandbox", Mint: mint.PublicKey().String(),
		MintAuthority: authority.PublicKey().String(),
		Demo402PayTo:  payTo.String(), FaucetWallet: faucet.PublicKey().String(),
		Facilitator: facilitator.PublicKey().String(),
		CreatedAt:   time.Now().UTC(),
	}
	if err := writeState(dir, s); err != nil {
		return err
	}

	fmt.Printf("\nsandbox ready\n  mint        %s\n  payTo       %s\n  faucet      %s\n"+
		"  facilitator %s\n",
		s.Mint, s.Demo402PayTo, s.FaucetWallet, s.Facilitator)
	fmt.Printf("\nThe services read %s, so they cannot disagree about which mint is USDC here.\n",
		filepath.Join(dir, stateFile))
	return nil
}

// rpcMessage renders a Solana RPC failure as a sentence.
//
// solana-go's RPCError.Error() returns a spew dump of the struct — a dozen lines of Go syntax with
// a pointer address in them — so a one-line "you have hit the faucet limit" arrives looking like a
// panic. The information anybody needs is the code and the message.
func rpcMessage(err error) string {
	var rpcErr *jsonrpc.RPCError
	if errors.As(err, &rpcErr) {
		return fmt.Sprintf("%s (code %d)", strings.TrimSpace(rpcErr.Message), rpcErr.Code)
	}
	if err == nil {
		return "none"
	}
	return err.Error()
}

// rateLimited reports whether a failure is the faucet saying "not today".
//
// It is worth distinguishing because it is not a defect and not a misconfiguration: it is the
// expected answer from a public cluster, and reporting it with the same weight as a real RPC
// failure trains people to ignore both.
func rateLimited(err error) bool {
	var rpcErr *jsonrpc.RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == 429
}

// fundingTarget is how much SOL each of the three keys should hold.
//
// Two SOL by default, which is what a local validator grants without complaint and roughly what a
// public airdrop caps at. It is settable because on devnet the operator is spending a real, rate
// limited allowance: six SOL across three keys is a day's worth from https://faucet.solana.com,
// and a demo that only ever creates a mint, one token account and a few transfers does not need
// anything close to it. Lowering it is the operator's call, so it is a variable rather than a
// number somebody has to patch.
//
//	BOOTSTRAP_FUND_SOL=0.2 leashctl bootstrap
//
// The floor is what the chain itself demands: below rent exemption for a mint account, funding
// "succeeds" and the very next instruction fails for a reason that does not name this.
func fundingTarget() (uint64, error) {
	raw := env("BOOTSTRAP_FUND_SOL", "2")
	sol, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("BOOTSTRAP_FUND_SOL: %q is not a number of SOL", raw)
	}
	lamports := uint64(sol * float64(solana.LAMPORTS_PER_SOL))
	const floor = uint64(solana.LAMPORTS_PER_SOL / 100) // 0.01 SOL
	if lamports < floor {
		return 0, fmt.Errorf("BOOTSTRAP_FUND_SOL: %s SOL is not enough to create a mint and pay "+
			"its fees. The minimum is 0.01", raw)
	}
	return lamports, nil
}

// mintExists asks the chain whether an account is really there.
func mintExists(ctx context.Context, c *solrpc.Client, addr string) (bool, error) {
	key, err := solana.PublicKeyFromBase58(addr)
	if err != nil {
		return false, err
	}
	// Confirmed, like every other read here. GetAccountInfo defaults to FINALIZED, which lags by
	// roughly a slot or two — long enough that a mint created moments ago reads as absent. For
	// `keys` and `faucet` that means briefly reporting a working sandbox as unprepared; for
	// bootstrap itself it would mean deciding the ledger had been reset and creating a SECOND
	// mint while the first was still settling.
	res, err := c.GetAccountInfoWithOpts(ctx, key, &solrpc.GetAccountInfoOpts{
		Commitment: solrpc.CommitmentConfirmed,
	})
	if err != nil {
		if errors.Is(err, solrpc.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return res != nil && res.Value != nil, nil
}

// fund makes sure a key has enough SOL, and says what to do when it cannot.
//
// A local validator grants airdrops freely. A PUBLIC network does not: devnet's faucet is rate
// limited and frequently dry, and mainnet has no faucet at all. So this asks for an airdrop, and
// if that is refused it checks whether the key is already funded — because on any network but a
// local one, funding is something an operator does out of band, once.
//
// The failure message names the address and the amount. A bootstrap that stops with "airdrop
// failed" leaves somebody guessing; one that says "send 2 SOL to this address" does not.
type funding struct {
	who  string
	key  solana.PublicKey
	want uint64
}

// fundSandbox gets SOL to every key that spends it, through ONE address on a public network.
//
// The mint authority is the treasury. That is not an arbitrary choice: it is the key that must
// hold SOL under every circumstance — it pays rent on the mint, rent on each token account, and
// the fee on every faucet grant — so it is going to need funding whatever else happens. Making it
// the single funded address means the others can be paid from it.
//
// Why this shape rather than one airdrop per key: on a local validator the difference is nothing,
// both work. On a public cluster it is the whole experience. faucet.solana.com is rate limited per address
// AND per IP, so asking an operator for three addresses is asking for three requests that are
// individually likely to be refused, spread over a day, before anything works. One address is one
// request, and bootstrap spreads it.
//
// It still tries the airdrop first, because on a local validator that succeeds instantly and no
// operator should be reading about faucets at all.
func fundSandbox(ctx context.Context, c *solrpc.Client, treasury *solana.Wallet,
	dependants []funding, want uint64) error {
	// What the treasury must hold: its own working balance, plus whatever the dependants are
	// short. Asking for exactly this and no more keeps the airdrop within what devnet grants.
	need := want
	var short []funding
	for _, d := range dependants {
		have := balanceOf(ctx, c, d.key)
		if have >= d.want {
			fmt.Printf("  %s already funded (%.3f SOL)\n", d.who,
				float64(have)/float64(solana.LAMPORTS_PER_SOL))
			continue
		}
		d.want -= have
		short = append(short, d)
		need += d.want
	}

	airdropErr := fundOne(ctx, c, funding{"mint authority", treasury.PublicKey(), need})
	if have := balanceOf(ctx, c, treasury.PublicKey()); have < need {
		var b strings.Builder
		fmt.Fprintf(&b, "this network will not fund the sandbox, and its treasury holds %.4f "+
			"SOL.\n\n", float64(have)/float64(solana.LAMPORTS_PER_SOL))
		fmt.Fprintf(&b, "    send at least %.2f SOL to  %s\n",
			float64(need-have)/float64(solana.LAMPORTS_PER_SOL), treasury.PublicKey())
		fmt.Fprintf(&b, "\n  ONE address. The mint authority is the sandbox's treasury: bootstrap "+
			"pays the other keys from it, so this is the only transfer anybody has to make.\n\n")
		// Where the SOL can come from differs by cluster, and the wrong answer wastes an
		// afternoon: faucet.solana.com serves devnet and not testnet. Asking the chain which one
		// it is beats printing advice that cannot work — the same helper `leashctl keys` uses, so
		// the two cannot give contradictory instructions.
		for _, line := range strings.Split(fundingAdvice(ctx, c), "\n") {
			fmt.Fprintf(&b, "  %s\n", line)
		}
		fmt.Fprintf(&b, "\n  The key is already written to the state directory, so fund that "+
			"address once and run this again; bootstrap is idempotent and will carry on from "+
			"here. Do NOT `make clean` in between — that destroys the state volume, and the next "+
			"run generates a different address.\n")
		// A rate limit is the expected answer from a public faucet, not a fault, so it gets one
		// line rather than a dump. Anything else might be a real problem and is shown in full.
		if rateLimited(airdropErr) {
			fmt.Fprintf(&b, "\n  (the cluster's own faucet is rate limited, as expected)")
		} else {
			fmt.Fprintf(&b, "\n  (last RPC error: %s)", rpcMessage(airdropErr))
		}
		return errors.New(b.String())
	}

	for _, d := range short {
		fmt.Printf("  paying %s %.3f SOL from the treasury\n", d.who,
			float64(d.want)/float64(solana.LAMPORTS_PER_SOL))
		if err := transferSOL(ctx, c, treasury, d.key, d.want); err != nil {
			return fmt.Errorf("paying the %s from the treasury: %w", d.who, err)
		}
	}
	return nil
}

// balanceOf reads a balance and treats an unreadable one as zero, because every caller here is
// deciding whether to TOP UP — and topping up an account we could not read is harmless, while
// skipping one we could not read is not.
func balanceOf(ctx context.Context, c *solrpc.Client, key solana.PublicKey) uint64 {
	bal, err := c.GetBalance(ctx, key, solrpc.CommitmentConfirmed)
	if err != nil || bal == nil {
		return 0
	}
	return bal.Value
}

// transferSOL moves lamports between two of the sandbox's own keys.
func transferSOL(ctx context.Context, c *solrpc.Client, from *solana.Wallet,
	to solana.PublicKey, lamports uint64) error {
	return submit(ctx, c, []solana.Instruction{
		system.NewTransferInstruction(lamports, from.PublicKey(), to).Build(),
	}, from)
}

// fundOne tops a key up, and settles for what it already holds if the faucet refuses.
func fundOne(ctx context.Context, c *solrpc.Client, n funding) error {
	if bal, err := c.GetBalance(ctx, n.key, solrpc.CommitmentConfirmed); err == nil {
		if bal.Value >= n.want {
			fmt.Printf("  %s already funded (%.3f SOL)\n", n.who,
				float64(bal.Value)/float64(solana.LAMPORTS_PER_SOL))
			return nil
		}
	}

	sig, err := c.RequestAirdrop(ctx, n.key, n.want, solrpc.CommitmentConfirmed)
	if err == nil {
		if err := waitForSignature(ctx, c, sig); err == nil {
			return nil
		}
	}

	// The airdrop was refused. Perhaps it is already funded enough to proceed anyway.
	bal, balErr := c.GetBalance(ctx, n.key, solrpc.CommitmentConfirmed)
	if balErr == nil && bal.Value > 0 {
		fmt.Printf("  %s could not be topped up, but holds %.3f SOL — continuing\n", n.who,
			float64(bal.Value)/float64(solana.LAMPORTS_PER_SOL))
		return nil
	}
	if err == nil {
		err = balErr
	}
	return err
}

// waitForSignature is a read-back, in the same spirit as everything else here: a submitted
// transaction is not a confirmed one, and bootstrap must not proceed on the assumption that it is.
func waitForSignature(ctx context.Context, c *solrpc.Client, sig solana.Signature) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		out, err := c.GetSignatureStatuses(ctx, true, sig)
		if err == nil && len(out.Value) > 0 && out.Value[0] != nil {
			if out.Value[0].Err != nil {
				return fmt.Errorf("transaction %s failed: %v", sig, out.Value[0].Err)
			}
			if out.Value[0].ConfirmationStatus == solrpc.ConfirmationStatusConfirmed ||
				out.Value[0].ConfirmationStatus == solrpc.ConfirmationStatusFinalized {
				return nil
			}
		}
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("transaction %s was not confirmed within 60s", sig)
}

// systemCreate is the create-account instruction, factored out so the demo can reuse it.
func systemCreate(rent uint64, payer, newAccount solana.PublicKey) solana.Instruction {
	return system.NewCreateAccountInstruction(rent, 165, solana.TokenProgramID,
		payer, newAccount).Build()
}

func createMint(ctx context.Context, c *solrpc.Client, authority, mint *solana.Wallet) error {
	rent, err := c.GetMinimumBalanceForRentExemption(ctx, 82, solrpc.CommitmentConfirmed)
	if err != nil {
		return err
	}
	ixs := []solana.Instruction{
		system.NewCreateAccountInstruction(rent, 82, solana.TokenProgramID,
			authority.PublicKey(), mint.PublicKey()).Build(),
		token.NewInitializeMint2Instruction(usdcDecimal, authority.PublicKey(),
			authority.PublicKey(), mint.PublicKey()).Build(),
	}
	return submit(ctx, c, ixs, authority, mint)
}

// createRecipientATA makes the associated token account every x402 client will derive.
//
// Idempotent, so a second bootstrap against a surviving ledger is not an error.
func createRecipientATA(ctx context.Context, c *solrpc.Client, payer *solana.Wallet,
	owner, mint solana.PublicKey) error {
	return submit(ctx, c, []solana.Instruction{
		ata.NewCreateIdempotentInstruction(payer.PublicKey(), owner, mint).Build(),
	}, payer)
}

func submit(ctx context.Context, c *solrpc.Client, ixs []solana.Instruction,
	payer *solana.Wallet, extra ...*solana.Wallet) error {
	bh, err := c.GetLatestBlockhash(ctx, solrpc.CommitmentConfirmed)
	if err != nil {
		return err
	}
	tx, err := solana.NewTransaction(ixs, bh.Value.Blockhash,
		solana.TransactionPayer(payer.PublicKey()))
	if err != nil {
		return err
	}
	signers := append([]*solana.Wallet{payer}, extra...)
	if _, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		for _, w := range signers {
			if w.PublicKey().Equals(key) {
				return &w.PrivateKey
			}
		}
		return nil
	}); err != nil {
		return err
	}
	sig, err := c.SendTransactionWithOpts(ctx, tx, solrpc.TransactionOpts{
		SkipPreflight: false, PreflightCommitment: solrpc.CommitmentConfirmed,
	})
	if err != nil {
		return err
	}
	// Submitted is not confirmed. Bootstrap reads back before proceeding, for the same reason
	// everything else here does: the next step depends on this one having actually happened.
	return waitForSignature(ctx, c, sig)
}

// loadOrCreateKey keeps a keypair on disk so a restart reuses the same sandbox rather than minting
// a second USDC that nothing else knows about.
func loadOrCreateKey(path string) (*solana.Wallet, error) {
	if b, err := os.ReadFile(path); err == nil {
		var raw []byte
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		pk := solana.PrivateKey(raw)
		return &solana.Wallet{PrivateKey: pk}, nil
	}
	w := solana.NewWallet()
	b, err := json.Marshal([]byte(w.PrivateKey))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	return w, nil
}

func readState(dir string) (State, error) {
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		return State{}, err
	}
	var s State
	err = json.Unmarshal(b, &s)
	return s, err
}

func writeState(dir string, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, stateFile), b, 0o644)
}

var _ = store.Store{}
