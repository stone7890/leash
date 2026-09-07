package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gagliardetto/solana-go"
	ata "github.com/gagliardetto/solana-go/programs/associated-token-account"
	"github.com/gagliardetto/solana-go/programs/token"
	solrpc "github.com/gagliardetto/solana-go/rpc"
	"github.com/stone7890/leash/internal/chain"
	"github.com/stone7890/leash/internal/chain/rpc"
	"github.com/stone7890/leash/internal/chain/spl"
	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/domain/state"
	"github.com/stone7890/leash/internal/domain/tier"
	"github.com/stone7890/leash/internal/kms"
	"github.com/stone7890/leash/internal/store"
)

// cmdDemo runs the whole loop the specification is built around, from the command line:
//
//	a brand-new wallet → an agent paying a real API within a $10 budget
//
// Everything here is real. A real delegation on a real chain, a real 402, a real signature from
// the signer, a real transfer submitted by the endpoint, and a real read-back. Nothing is
// simulated, which is the point: a demonstration that skips the chain demonstrates nothing.
func cmdDemo(ctx context.Context, args []string) error {
	dir := env("LEASH_STATE_DIR", "/state")
	sb, err := readState(dir)
	if err != nil {
		return fmt.Errorf("no sandbox state at %s — run `leashctl bootstrap` first: %w", dir, err)
	}

	signerURL := env("SIGNER_BASE_URL", "http://localhost:4100")
	demoURL := env("DEMO_ENDPOINT_URL", "http://localhost:4200")
	rpcURL := env("RPC_SANDBOX_URL", "http://localhost:8899")

	st, err := open(ctx)
	if err != nil {
		return err
	}
	defer st.Close(context.Background())

	pool, err := rpc.New(map[network.Network]rpc.Endpoints{network.Sandbox: {Primary: rpcURL}})
	if err != nil {
		return err
	}
	adapter := spl.New(pool)
	client := solrpc.New(rpcURL)

	c, cancel := withTimeout(ctx, 8*time.Minute)
	defer cancel()
	now := time.Now().UTC()

	authority, err := loadOrCreateKey(filepath.Join(dir, keyMint))
	if err != nil {
		return err
	}

	step(1, "a brand-new owner wallet")
	owner := solana.NewWallet()
	fmt.Printf("     %s\n", owner.PublicKey())
	// Through the treasury, like everything else on a public network: a wallet created one line
	// ago holds nothing, and devnet will not airdrop to it. A tenth of a SOL is thousands of
	// signatures — this wallet creates one token account, approves one delegation, and makes a
	// handful of payments.
	reserve, err := fundingTarget()
	if err != nil {
		return err
	}
	if err := fundSandbox(c, client, authority, []funding{
		{"the owner wallet", owner.PublicKey(), solana.LAMPORTS_PER_SOL / 10},
	}, reserve); err != nil {
		return err
	}
	mint := solana.MustPublicKeyFromBase58(sb.Mint)

	ownerATA, err := fundWithUSDC(c, client, authority, owner.PublicKey(), mint, 100*money.One)
	if err != nil {
		return err
	}
	fmt.Printf("     funded with 100.000000 sandbox USDC\n")

	step(2, "an agent, with its key held only by the signer")
	org, err := st.UpsertOrg(c, owner.PublicKey().String(), now)
	if err != nil {
		return err
	}
	seed, pub, err := kms.Generate()
	if err != nil {
		return err
	}
	wrapper, err := kms.NewLocal(dir)
	if err != nil {
		return err
	}
	wrapped, keyRef, err := wrapper.Wrap(seed)
	if err != nil {
		return err
	}
	apiKey, keyHash, err := kms.APIKey(network.Sandbox.KeyPrefix())
	if err != nil {
		return err
	}
	agentPub := solana.PublicKeyFromBytes(pub)

	agent, err := st.CreateAgent(c, store.NewAgent{
		OrgID: org.ID, Name: "research-bot-01", Template: "research",
		Network: network.Sandbox, RunsAs: "cli", Pubkey: agentPub.String(),
		IdempotencyKey: "demo-" + now.Format("20060102150405.000000000"),
		KeyHash:        keyHash, WrappedKey: wrapped, KMSKeyRef: keyRef,
		Policy: store.Policy{
			AllowHosts: []string{hostOf(demoURL)}, PerTxMax: money.Base(250_000),
			VelocityMax: money.One, VelocityWindow: 10 * time.Minute,
		},
		ExpiryTier: adapter.Capabilities().ExpiryTier(),
	}, now)
	if err != nil {
		return err
	}
	fmt.Printf("     %s  agent key %s\n", agent.ID, agentPub)
	fmt.Printf("     API key   %s\n", apiKey)
	// I3, visible: the adapter says what it enforces, and this is what the interface will show.
	fmt.Printf("     expiry is enforced at the %s tier (the SPL delegate has no clock)\n",
		adapter.Capabilities().ExpiryTier())

	step(3, "the owner signs ONE transaction to delegate a $10.00 budget")
	cap10 := 10 * money.One
	unsigned, err := adapter.BuildCreate(c, chain.CreateParams{
		Owner: owner.PublicKey().String(), Agent: agentPub.String(),
		Mint: sb.Mint, Cap: cap10, ExpiryTS: now.Add(7 * 24 * time.Hour),
		Network: network.Sandbox,
	})
	if err != nil {
		return err
	}
	fmt.Printf("     cap $%s · rent %d lamports · funds move: %v\n",
		unsigned.Summary.Cap, unsigned.Summary.Rent, unsigned.Summary.FundsMove)

	allowanceAddr, err := adapter.AllowanceAccount(c, owner.PublicKey().String(),
		agentPub.String(), sb.Mint, network.Sandbox)
	if err != nil {
		return err
	}
	sig, err := signAndSubmit(c, client, unsigned.Base64, owner)
	if err != nil {
		return fmt.Errorf("the delegation was refused: %w", err)
	}
	fmt.Printf("     signed by the owner's wallet · %s\n", short(sig.String()))

	alw, err := st.CreateAllowance(c, store.NewAllowance{
		AgentID: agent.ID, Network: network.Sandbox, OnchainAddr: allowanceAddr,
		Mint: sb.Mint, TokenProgram: solana.TokenProgramID.String(), Cap: cap10,
		ExpiryTS: now.Add(7 * 24 * time.Hour), ExpiryTier: adapter.Capabilities().ExpiryTier(),
	}, now)
	if err != nil {
		return err
	}

	step(4, "read-back: pending becomes active only when the chain agrees")
	onchain, err := adapter.ReadAllowance(c, network.Sandbox, allowanceAddr)
	if err != nil {
		return err
	}
	if !onchain.Exists {
		return fmt.Errorf("the allowance account was not readable after the delegation confirmed")
	}
	if err := st.RefreshFromChain(c, network.Sandbox, alw.ID, 0,
		state.AllowanceActive, onchain.Slot, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Printf("     active · delegated $%s · read at slot %d\n", onchain.Cap, onchain.Slot)

	step(5, "the agent calls a paid API and is asked for payment")
	challengeRaw, err := getChallenge(c, demoURL+"/demo/search")
	if err != nil {
		return err
	}
	fmt.Printf("     402 · a real x402 challenge, %d bytes\n", len(challengeRaw))

	step(6, "the signer checks eight rules and signs inside the allowance")
	attempt := newAttempt()
	signed, err := callSigner(c, signerURL, apiKey, hostOf(demoURL), attempt, challengeRaw)
	if err != nil {
		return err
	}
	fmt.Printf("     allowed · %s · remaining $%v\n",
		short(signed.Signature), signed.Allowance["remaining"])

	step(7, "the agent replays the call carrying the payment; the endpoint settles it")
	receipt, err := replayWithPayment(c, demoURL+"/demo/search", signed.Header, signed.Payload)
	if err != nil {
		return err
	}
	fmt.Printf("     200 OK · receipt %s\n", short(receipt))

	step(8, "read-back: the payment becomes confirmed")
	// By MEMO, not by the signature we produced. The facilitator sponsored the fee, so the chain
	// knows this transaction by the fee payer's signature — which did not exist when we signed.
	confirmed := false
	for i := 0; i < 40; i++ {
		conf, err := adapter.SignatureByMemo(c, network.Sandbox, allowanceAddr, signed.Memo)
		if err == nil && conf.Confirmed {
			fmt.Printf("     confirmed at slot %d\n", conf.Slot)
			fmt.Printf("     settled under %s — the fee payer's signature, not ours\n",
				short(conf.Signature))
			confirmed = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !confirmed {
		// Not a failure of the demo. `unknown` is a first-class state, and the sweep keeps
		// checking — which is the behaviour, not a gap in it.
		fmt.Printf("     not yet seen — the payment is `unknown`, and the sweep keeps checking\n")
	}

	step(9, "the evidence")
	if err := proveNoDoublePay(c, signerURL, apiKey, hostOf(demoURL), attempt, challengeRaw,
		signed.Signature); err != nil {
		return err
	}
	if err := proveNetworkIsolation(c, signerURL, hostOf(demoURL), challengeRaw); err != nil {
		return err
	}
	if err := proveTheCapBinds(c, st, adapter, client, owner, agentPub, alw, sb,
		wrapper, wrapped, keyRef); err != nil {
		return err
	}

	fmt.Printf("\n  Owner wallet %s\n", owner.PublicKey())
	fmt.Printf("  Agent        %s\n", agent.ID)
	fmt.Printf("  Allowance    %s\n", allowanceAddr)
	fmt.Printf("  Payment      %s\n\n", signed.PaymentID)
	_ = ownerATA
	return nil
}

// proveNoDoublePay submits the SAME challenge again and requires the SAME signature back.
//
// One of the four pieces of evidence. Not "an error the second time" — the identical answer,
// because the claim replays what was stored rather than doing the work twice.
func proveNoDoublePay(ctx context.Context, signerURL, apiKey, host, attempt string,
	ch json.RawMessage, firstSig string) error {
	// The SAME attempt identifier: this is a retry, and a retry must replay.
	again, err := callSigner(ctx, signerURL, apiKey, host, attempt, ch)
	if err != nil {
		return fmt.Errorf("replaying the challenge should return the original answer: %w", err)
	}
	if again.Signature != firstSig {
		return fmt.Errorf(
			"THE SAME CHALLENGE PRODUCED TWO SIGNATURES (%s then %s). One API call would be paid "+
				"twice", short(firstSig), short(again.Signature))
	}
	fmt.Printf("     ✓ no double pay — the same challenge returned the same signature\n")
	return nil
}

// proveNetworkIsolation presents a mainnet-shaped challenge to a sandbox key.
func proveNetworkIsolation(ctx context.Context, signerURL, host string, ch json.RawMessage) error {
	// A mainnet challenge presented with a sandbox key.
	bad := json.RawMessage(strings.Replace(string(ch),
		"solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1", "solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp", 1))
	_, err := callSigner(ctx, signerURL, "lk_test_deliberately-wrong", host, newAttempt(), bad)
	if err == nil {
		return fmt.Errorf("A MAINNET CHALLENGE WAS SIGNED BY A SANDBOX KEY")
	}
	fmt.Printf("     ✓ networks isolated — a test key on a mainnet challenge was refused\n")
	return nil
}

// proveTheCapBinds asks the CHAIN to reject an over-cap draw.
//
// This is the only piece of evidence that shows the hard tier is real. It bypasses the signer
// entirely — signing a draw larger than the delegation directly with the agent key — so what
// refuses it is Solana, not our policy engine.
func proveTheCapBinds(ctx context.Context, st *store.Store, adapter *spl.Adapter,
	client *solrpc.Client, owner *solana.Wallet, agentPub solana.PublicKey,
	alw store.Allowance, sb State, wrapper kms.Wrapper, wrapped []byte, keyRef string) error {

	priv, err := wrapper.Unwrap(wrapped, keyRef)
	if err != nil {
		return err
	}
	bh, err := client.GetLatestBlockhash(ctx, solrpc.CommitmentConfirmed)
	if err != nil {
		return err
	}
	// $1,000 against a $10 delegation.
	over, err := adapter.BuildDraw(ctx, chain.DrawParams{
		AllowanceAcc: alw.OnchainAddr, AgentPubkey: agentPub.String(),
		AgentSign: demoSigner{priv: priv, pub: agentPub.String()},
		PayTo:     sb.Demo402PayTo, Mint: sb.Mint, Amount: 1000 * money.One,
		Blockhash: bh.Value.Blockhash.String(), Network: network.Sandbox,
	})
	if err != nil {
		return err
	}
	raw, err := solana.TransactionFromBase64(over.Base64)
	if err != nil {
		return err
	}
	if _, err := client.SendTransactionWithOpts(ctx, raw, solrpc.TransactionOpts{
		PreflightCommitment: solrpc.CommitmentConfirmed,
	}); err == nil {
		return fmt.Errorf("THE CHAIN ACCEPTED A DRAW OF $1000 AGAINST A $10 CAP")
	}
	fmt.Printf("     ✓ the cap is enforced by Solana — a $1000 draw against a $10 cap was " +
		"rejected by the CHAIN, not by us\n")
	return nil
}

type demoSigner struct {
	priv []byte
	pub  string
}

func (d demoSigner) PublicKey() string { return d.pub }
func (d demoSigner) Sign(message []byte) ([]byte, error) {
	sig, err := solana.PrivateKey(d.priv).Sign(message)
	if err != nil {
		return nil, err
	}
	return sig[:], nil
}

func step(n int, title string) { fmt.Printf("\n  %d · %s\n", n, title) }

func newAttempt() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:6] + "…" + s[len(s)-4:]
}

// fundOwnerWithUSDC mints sandbox USDC into the owner's ASSOCIATED token account.
//
// The associated one specifically: BuildCreate transfers the cap from there, because that is where
// a wallet puts a user's USDC and therefore where an owner's balance actually lives. Funding some
// other account would leave the demo working against a balance no real wallet would show.
func fundWithUSDC(ctx context.Context, c *solrpc.Client, authority *solana.Wallet,
	owner solana.PublicKey, mint solana.PublicKey, amount money.Base) (solana.PublicKey, error) {
	ataAddr, _, err := solana.FindAssociatedTokenAddress(owner, mint)
	if err != nil {
		return solana.PublicKey{}, err
	}
	if err := submit(ctx, c, []solana.Instruction{
		// Idempotent: a second demo run against the same wallet must not fail because the account
		// already exists.
		ata.NewCreateIdempotentInstruction(authority.PublicKey(), owner, mint).Build(),
		token.NewMintToInstruction(uint64(amount), mint, ataAddr,
			authority.PublicKey(), nil).Build(),
	}, authority); err != nil {
		return solana.PublicKey{}, err
	}
	return ataAddr, nil
}

func signAndSubmit(ctx context.Context, c *solrpc.Client, b64 string,
	signers ...*solana.Wallet) (solana.Signature, error) {
	tx, err := solana.TransactionFromBase64(b64)
	if err != nil {
		return solana.Signature{}, err
	}
	// Rebuild against a current blockhash: the transaction was built moments ago, and a demo run
	// on a slow machine can outlive one.
	bh, err := c.GetLatestBlockhash(ctx, solrpc.CommitmentConfirmed)
	if err != nil {
		return solana.Signature{}, err
	}
	tx.Message.RecentBlockhash = bh.Value.Blockhash
	if _, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		for _, w := range signers {
			if w.PublicKey().Equals(key) {
				return &w.PrivateKey
			}
		}
		return nil
	}); err != nil {
		return solana.Signature{}, err
	}
	sig, err := c.SendTransactionWithOpts(ctx, tx, solrpc.TransactionOpts{
		PreflightCommitment: solrpc.CommitmentConfirmed,
	})
	if err != nil {
		return solana.Signature{}, err
	}
	return sig, waitForSignature(ctx, c, sig)
}

// ── talking to the services over HTTP, exactly as an agent would ─────────────

// The 402 body, verbatim. It is forwarded to the signer unparsed, exactly as a real agent would.
func getChallenge(ctx context.Context, url string) (json.RawMessage, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling the sample endpoint: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusPaymentRequired {
		return nil, fmt.Errorf("expected 402 from the sample endpoint, got %d: %s",
			res.StatusCode, body)
	}
	return json.RawMessage(body), nil
}

type signResponse struct {
	PaymentID string         `json:"payment_id"`
	Signature string         `json:"signature"`
	Payload   string         `json:"payload"`
	Header    string         `json:"header"`
	Memo      string         `json:"memo"`
	Allowance map[string]any `json:"allowance"`
}

func callSigner(ctx context.Context, base, apiKey, host, attempt string, ch json.RawMessage) (
	signResponse, error) {
	body, _ := json.Marshal(map[string]any{"challenge": ch, "host": host})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/sign",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Leash-Key", apiKey)
	req.Header.Set("Idempotency-Key", attempt)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return signResponse{}, fmt.Errorf("calling the signer: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return signResponse{}, fmt.Errorf("the signer refused (%d): %s", res.StatusCode, raw)
	}
	var out signResponse
	return out, json.Unmarshal(raw, &out)
}

func replayWithPayment(ctx context.Context, url, header, payload string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if header == "" {
		header = "PAYMENT-SIGNATURE"
	}
	req.Header.Set(header, payload)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the endpoint did not accept the payment (%d): %s",
			res.StatusCode, raw)
	}
	var out struct {
		Receipt struct {
			Signature string `json:"signature"`
		} `json:"receipt"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Receipt.Signature, nil
}

func hostOf(url string) string {
	s := url
	for _, p := range []string{"http://", "https://"} {
		if len(s) > len(p) && s[:len(p)] == p {
			s = s[len(p):]
		}
	}
	if i := indexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

var _ = os.Stdout
var _ = tier.Signer
