package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gagliardetto/solana-go"
	ata "github.com/gagliardetto/solana-go/programs/associated-token-account"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/programs/token"
	solrpc "github.com/gagliardetto/solana-go/rpc"
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
	// All three are attempted before any of them reports, so an operator on a public cluster gets
	// one list of addresses to fund rather than discovering the next one on the next run. Same
	// principle as the configuration loader naming every missing variable at once: fixing one item
	// per restart is a bad afternoon, and here each restart also costs a rate-limited faucet.
	if err := fundAll(c, client, []funding{
		{"mint authority", authority.PublicKey(), 2 * solana.LAMPORTS_PER_SOL},
		{"faucet", faucet.PublicKey(), 2 * solana.LAMPORTS_PER_SOL},
		{"sample endpoint's facilitator", facilitator.PublicKey(), 2 * solana.LAMPORTS_PER_SOL},
	}); err != nil {
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

// mintExists asks the chain whether an account is really there.
func mintExists(ctx context.Context, c *solrpc.Client, addr string) (bool, error) {
	key, err := solana.PublicKeyFromBase58(addr)
	if err != nil {
		return false, err
	}
	res, err := c.GetAccountInfo(ctx, key)
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

// fundAll attempts every key and reports all the ones it could not fund, together.
func fundAll(ctx context.Context, c *solrpc.Client, needs []funding) error {
	var short []funding
	var last error
	for _, n := range needs {
		if err := fundOne(ctx, c, n); err != nil {
			short = append(short, n)
			last = err
		}
	}
	if len(short) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "this network will not fund %d of the keys this sandbox needs, and they hold "+
		"nothing.\n\n", len(short))
	for _, n := range short {
		fmt.Fprintf(&b, "    send at least %.2f SOL to  %s   (%s)\n",
			float64(n.want)/float64(solana.LAMPORTS_PER_SOL), n.key, n.who)
	}
	fmt.Fprintf(&b, "\n  A local validator airdrops freely; a public one does not — devnet's "+
		"faucet is rate limited (https://faucet.solana.com) and mainnet has none. The keys are "+
		"already written to the state directory, so fund these addresses once and run this again; "+
		"bootstrap is idempotent and will carry on from here.\n  (last RPC error: %v)", last)
	return errors.New(b.String())
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
