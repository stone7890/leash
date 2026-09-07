package indexer

import (
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stone7890/leash/internal/chain"
	"github.com/stone7890/leash/internal/domain/fault"
	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
)

// Unsigned transaction building, on an internal path.
//
// It lives HERE, in Go, rather than in the dashboard's TypeScript, for one reason: there must be
// exactly one implementation of what a delegation transaction looks like. The adapter is the file
// where a wrong assumption costs money rather than a test failure — docs/08-onchain-adapter.md
// says so, and a pull request touching it must attach a sandbox end-to-end run. Two copies in two
// languages would double that risk and halve the confidence.
//
// The dashboard still OWNS the endpoint, in the sense the deck means: it authenticates the owner,
// decides what may be built, and hands the result to the wallet. It delegates the construction to
// the process that owns the adapter. Registered in docs/16-deck-conformance.md.

type intentRequest struct {
	Kind         string `json:"kind"` // delegation | modify | revoke
	Owner        string `json:"owner"`
	Agent        string `json:"agent"`
	Network      string `json:"network"`
	Cap          string `json:"cap"`
	ExpiryDays   int    `json:"expiry_days"`
	AllowanceAcc string `json:"allowance_account"`
	SweepTo      string `json:"sweep_to"`
}

type intentResponse struct {
	Transaction      string    `json:"transaction"`
	Blockhash        string    `json:"blockhash"`
	ExpiresAt        time.Time `json:"expires_at"`
	AllowanceAccount string    `json:"allowance_account"`
	Summary          summary   `json:"summary"`
}

type summary struct {
	Cap        string    `json:"cap"`
	ExpiryTS   time.Time `json:"expiry_ts"`
	DelegateTo string    `json:"delegate_to"`
	NetworkFee string    `json:"network_fee"`
	RentSOL    string    `json:"rent_sol"`
	// FundsMove is TRUE for a delegation under the SPL implementation, and the onboarding copy has
	// to say so. "Funds stay put" would be false: the owner's USDC moves into a per-agent account
	// they still own.
	FundsMove  bool   `json:"funds_move"`
	ExpiryTier string `json:"expiry_tier"`
	// The mint and its program travel with the intent so the dashboard records the allowance
	// against exactly what the transaction referenced, rather than looking them up again and
	// risking a different answer.
	Mint         string `json:"mint"`
	TokenProgram string `json:"token_program"`
}

func (ix *Indexer) buildIntent(c *gin.Context) {
	if !ix.internalOK(c) {
		c.JSON(http.StatusUnauthorized, fault.Envelope{Error: fault.Body{
			Code: "UNAUTHENTICATED", Message: "internal token required", Retriable: false}})
		return
	}

	var body intentRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, fault.Envelope{Error: fault.Body{
			Code: "MALFORMED_REQUEST", Message: err.Error(), Retriable: false}})
		return
	}
	net, err := network.Parse(body.Network)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, fault.Envelope{Error: fault.Body{
			Code: "INVALID_NETWORK", Message: "network must be sandbox or mainnet", Retriable: false}})
		return
	}

	ctx := c.Request.Context()
	mint, program := ix.mintFor(net)
	caps := ix.adapter.Capabilities()

	var (
		tx      chain.UnsignedTx
		account string
	)

	switch body.Kind {
	case "delegation":
		cap, perr := money.Parse(body.Cap)
		if perr != nil {
			c.JSON(http.StatusUnprocessableEntity, fault.Envelope{Error: fault.Body{
				Code: "INVALID_AMOUNT", Message: perr.Error(), Field: "cap", Retriable: false}})
			return
		}
		days := body.ExpiryDays
		if days <= 0 {
			days = 7
		}
		account, err = ix.adapter.AllowanceAccount(ctx, body.Owner, body.Agent, mint, net)
		if err != nil {
			break
		}
		tx, err = ix.adapter.BuildCreate(ctx, chain.CreateParams{
			Owner: body.Owner, Agent: body.Agent, Mint: mint, Program: program,
			Cap: cap, ExpiryTS: time.Now().UTC().AddDate(0, 0, days), Network: net,
		})

	case "modify":
		cap, perr := money.Parse(body.Cap)
		if perr != nil {
			c.JSON(http.StatusUnprocessableEntity, fault.Envelope{Error: fault.Body{
				Code: "INVALID_AMOUNT", Message: perr.Error(), Field: "cap", Retriable: false}})
			return
		}
		account = body.AllowanceAcc
		days := body.ExpiryDays
		if days <= 0 {
			days = 7
		}
		tx, err = ix.adapter.BuildModify(ctx, chain.ModifyParams{
			Owner: body.Owner, Agent: body.Agent, AllowanceAcc: body.AllowanceAcc,
			Mint: mint, Program: program, NewCap: cap,
			NewExpiryTS: time.Now().UTC().AddDate(0, 0, days), Network: net,
		})

	case "revoke":
		account = body.AllowanceAcc
		// Two instructions, always: revoke AND sweep. Revoking alone leaves the owner's remaining
		// balance in an account they will not think to look at.
		tx, err = ix.adapter.BuildRevoke(ctx, chain.RevokeParams{
			Owner: body.Owner, AllowanceAcc: body.AllowanceAcc, Mint: mint,
			Program: program, Network: net, SweepTo: body.SweepTo,
		})

	default:
		c.JSON(http.StatusUnprocessableEntity, fault.Envelope{Error: fault.Body{
			Code: "MALFORMED_REQUEST", Message: "kind must be delegation, modify or revoke",
			Retriable: false}})
		return
	}

	if err != nil {
		c.JSON(http.StatusServiceUnavailable, fault.Envelope{Error: fault.Body{
			Code: "CHAIN_UNREACHABLE", Message: err.Error(), Retriable: true}})
		return
	}

	c.JSON(http.StatusAccepted, intentResponse{
		Transaction: tx.Base64, Blockhash: tx.Blockhash, ExpiresAt: tx.ExpiresAt,
		AllowanceAccount: account,
		Summary: summary{
			Cap: tx.Summary.Cap.String(), ExpiryTS: tx.Summary.ExpiryTS,
			DelegateTo: tx.Summary.DelegateTo,
			NetworkFee: lamports(tx.Summary.NetworkFee),
			RentSOL:    lamports(tx.Summary.Rent),
			FundsMove:  tx.Summary.FundsMove,
			// I3: the tier the adapter actually delivers, carried to the interface as data.
			ExpiryTier:   string(caps.ExpiryTier()),
			Mint:         mint,
			TokenProgram: program,
		},
	})
}

// submitSigned broadcasts a transaction the OWNER has signed in their wallet.
//
// Leash never signs it and never could — it arrives here already signed. What this does is put it
// on the network and hand back the signature, so the dashboard can wait for the read-back rather
// than guessing.
func (ix *Indexer) submitSigned(c *gin.Context) {
	if !ix.internalOK(c) {
		c.JSON(http.StatusUnauthorized, fault.Envelope{Error: fault.Body{
			Code: "UNAUTHENTICATED", Message: "internal token required", Retriable: false}})
		return
	}
	var body struct {
		Transaction string `json:"transaction"`
		Network     string `json:"network"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, fault.Envelope{Error: fault.Body{
			Code: "MALFORMED_REQUEST", Message: err.Error(), Retriable: false}})
		return
	}
	net, err := network.Parse(body.Network)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, fault.Envelope{Error: fault.Body{
			Code: "INVALID_NETWORK", Message: "network must be sandbox or mainnet", Retriable: false}})
		return
	}

	sig, err := ix.Submit(c.Request.Context(), net, body.Transaction)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, fault.Envelope{Error: fault.Body{
			Code: "CHAIN_UNREACHABLE", Message: err.Error(), Retriable: true}})
		return
	}
	// 202, never 200. There is no path by which a client can read "the API returned OK" as "the
	// money moved" — the allowance becomes active only after the read-back.
	c.JSON(http.StatusAccepted, gin.H{"signature": sig})
}

func (ix *Indexer) internalOK(c *gin.Context) bool {
	if ix.internalToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare(
		[]byte(c.GetHeader("X-Leash-Internal")), []byte(ix.internalToken)) == 1
}

// lamports renders a SOL amount for display. It is not USDC and does not go through money.Base,
// which is deliberately about micro-USDC and nothing else.
func lamports(v money.Base) string {
	whole := int64(v) / 1_000_000_000
	frac := int64(v) % 1_000_000_000
	return itoa(whole) + "." + pad9(frac)
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func pad9(v int64) string {
	s := itoa(v)
	for len(s) < 9 {
		s = "0" + s
	}
	return s
}
