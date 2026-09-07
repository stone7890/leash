package demo402

import (
	"bytes"
	"fmt"

	"github.com/gagliardetto/solana-go"
)

// The structural checks a facilitator performs before it signs anything.
//
// Ported from pay-kit's `VerifyExactTransaction`, not invented. This is where the real security
// of x402 lives, and the reason is worth stating: THE FACILITATOR SIGNS THE WHOLE TRANSACTION.
// Its signature authorises every instruction in it, not only the payment — so a client that
// smuggles an extra instruction alongside a correct-looking payment gets the facilitator to
// authorise that too.
//
// pay-kit's own `docs/security/fee-payer-drain.md` describes exactly this: a transaction carrying
// the expected `transferChecked` AND a system transfer draining the fee payer passes any check
// that merely asks "is the payment there?", because the payment IS there. The defence is an
// instruction ALLOWLIST plus a source guard, which is what the layout below enforces.
//
// The layout is positional and the verifier reads it by index:
//
//	[0] ComputeBudget SetComputeUnitLimit
//	[1] ComputeBudget SetComputeUnitPrice   (<= 5,000,000 microlamports)
//	[2] transferChecked, to the recipient's ATA, for the exact amount
//	[3..] only Memo or Lighthouse; total in [3, 6]

var (
	computeBudgetProgram = solana.MustPublicKeyFromBase58("ComputeBudget111111111111111111111111111111")
	memoProgram          = solana.MustPublicKeyFromBase58("MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr")
	// Wallets inject guard instructions of their own — Phantom one, Solflare two. Refusing them
	// would refuse every payment made from a real wallet.
	lighthouseProgram = solana.MustPublicKeyFromBase58("L2TExMFKdjpN9kozasaurPirfHy9P8sbXoAN1qA3S95")
)

const (
	minInstructions = 3
	maxInstructions = 6
	// The cap exists so a client cannot make the facilitator pay an enormous priority fee.
	maxComputeUnitPriceMicroLamports = uint64(5_000_000)

	discriminatorSetComputeUnitLimit = byte(2)
	discriminatorSetComputeUnitPrice = byte(3)
	discriminatorTransferChecked     = byte(12)
)

// verify refuses anything that is not the exact shape agreed in the challenge.
func (s *Server) verify(tx *solana.Transaction) error {
	ix := tx.Message.Instructions
	if len(ix) < minInstructions || len(ix) > maxInstructions {
		return fmt.Errorf("instruction count is %d, outside the permitted range %d to %d",
			len(ix), minInstructions, maxInstructions)
	}

	keys := tx.Message.AccountKeys
	programOf := func(i int) (solana.PublicKey, error) {
		idx := int(ix[i].ProgramIDIndex)
		if idx >= len(keys) {
			return solana.PublicKey{}, fmt.Errorf("instruction %d names an account that is not present", i)
		}
		return keys[idx], nil
	}

	// [0] and [1] — the compute budget, by index.
	for i, want := range []byte{discriminatorSetComputeUnitLimit, discriminatorSetComputeUnitPrice} {
		prog, err := programOf(i)
		if err != nil {
			return err
		}
		if !prog.Equals(computeBudgetProgram) {
			return fmt.Errorf("instruction %d is not a compute-budget instruction", i)
		}
		if len(ix[i].Data) == 0 || ix[i].Data[0] != want {
			return fmt.Errorf("instruction %d is the wrong compute-budget instruction", i)
		}
	}
	// The price cap. Without it a client could set a fee the facilitator did not agree to pay.
	if len(ix[1].Data) >= 9 {
		price := le64(ix[1].Data[1:9])
		if price > maxComputeUnitPriceMicroLamports {
			return fmt.Errorf("the compute unit price of %d microlamports is over the cap of %d",
				price, maxComputeUnitPriceMicroLamports)
		}
	}

	// [2] — the payment itself.
	prog, err := programOf(2)
	if err != nil {
		return err
	}
	if !prog.Equals(solana.TokenProgramID) && !prog.Equals(solana.Token2022ProgramID) {
		return fmt.Errorf("instruction 2 is not a token instruction")
	}
	if len(ix[2].Data) < 9 || ix[2].Data[0] != discriminatorTransferChecked {
		// transferChecked specifically. A plain `transfer` carries no mint and no decimals, so the
		// chain cannot refuse an amount computed against the wrong precision.
		return fmt.Errorf("instruction 2 is not transferChecked")
	}
	amount := le64(ix[2].Data[1:9])
	if amount != uint64(s.price) {
		return fmt.Errorf("the payment is %d base units, and this resource costs %d",
			amount, uint64(s.price))
	}

	// transferChecked accounts: source, mint, destination, authority.
	if len(ix[2].Accounts) < 4 {
		return fmt.Errorf("instruction 2 does not name enough accounts")
	}
	accountAt := func(pos int) (solana.PublicKey, error) {
		idx := int(ix[2].Accounts[pos])
		if idx >= len(keys) {
			return solana.PublicKey{}, fmt.Errorf("instruction 2 names an account that is not present")
		}
		return keys[idx], nil
	}
	mintKey, err := accountAt(1)
	if err != nil {
		return err
	}
	destKey, err := accountAt(2)
	if err != nil {
		return err
	}
	authorityKey, err := accountAt(3)
	if err != nil {
		return err
	}

	wantMint, err := solana.PublicKeyFromBase58(s.mint)
	if err != nil {
		return fmt.Errorf("this endpoint's mint is not configured: %w", err)
	}
	if !mintKey.Equals(wantMint) {
		return fmt.Errorf("the payment is in a token this endpoint does not accept")
	}

	// The destination must be OUR associated token account. Anything else is a payment to
	// somebody else that happens to look right.
	recipient, err := solana.PublicKeyFromBase58(s.payTo)
	if err != nil {
		return fmt.Errorf("this endpoint's recipient is not configured: %w", err)
	}
	tokenProgram := solana.TokenProgramID
	if prog.Equals(solana.Token2022ProgramID) {
		tokenProgram = solana.Token2022ProgramID
	}
	wantDest, _, err := solana.FindProgramAddress(
		[][]byte{recipient.Bytes(), tokenProgram.Bytes(), wantMint.Bytes()},
		solana.SPLAssociatedTokenAccountProgramID,
	)
	if err != nil {
		return err
	}
	if !destKey.Equals(wantDest) {
		return fmt.Errorf("the payment is not addressed to this endpoint's token account")
	}

	// THE fee-payer-drain defence. The fee payer signs everything; if it were also the transfer
	// authority, a transaction could move the facilitator's own money and the facilitator would
	// have authorised it.
	if len(keys) == 0 {
		return fmt.Errorf("the transaction names no accounts")
	}
	if authorityKey.Equals(keys[0]) {
		return fmt.Errorf("the transfer authority is also the fee payer, which is refused")
	}

	// [3..] — the allowlist. Everything else is refused, which is the other half of the drain
	// defence: an extra instruction the facilitator would otherwise sign for.
	seenMemo := 0
	for i := 3; i < len(ix); i++ {
		prog, err := programOf(i)
		if err != nil {
			return err
		}
		switch {
		case prog.Equals(memoProgram):
			seenMemo++
		case prog.Equals(lighthouseProgram):
			// A wallet's own guard instruction.
		default:
			return fmt.Errorf("instruction %d is not permitted in a payment transaction", i)
		}
	}
	if seenMemo != 1 {
		// Exactly one. The memo IS the replay protection: x402 on Solana has no nonce field, so
		// two otherwise identical payments would be the same transaction without it.
		return fmt.Errorf("a payment carries exactly one memo, and this one carries %d", seenMemo)
	}
	return nil
}

func le64(b []byte) uint64 {
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

var _ = bytes.Equal
