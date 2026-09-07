// Package demo402 is the sample x402 endpoint used by onboarding step 5.
//
// It sells one thing for $0.01 in sandbox USDC, and it exists so that a brand-new owner sees a
// REAL payment happen — a real 402, a real challenge, a real signature, a real transaction — before
// they have found an endpoint of their own.
//
// It is a sandbox fixture and refuses to boot with mainnet configured. It must never take real
// money, because nothing it returns is worth any.
package demo402

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gagliardetto/solana-go"
	solrpc "github.com/gagliardetto/solana-go/rpc"
	"github.com/gin-gonic/gin"
	"github.com/stone7890/leash/internal/chain/rpc"
	"github.com/stone7890/leash/internal/domain/challenge"
	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
)

type Server struct {
	pool  *rpc.Pool
	mint  string
	payTo string
	price money.Base

	// This endpoint is its own facilitator: it sponsors the network fee and co-signs settlement.
	// pay-kit works the same way — its Go adapter refuses a FacilitatorURL outright, so a server
	// either does this itself or does not settle at all.
	feePayer     string
	tokenProgram string
	sign         func(message []byte) ([]byte, error)
}

type Options struct {
	Mint         string
	PayTo        string
	Price        money.Base
	FeePayer     string
	TokenProgram string
	// Sign adds the facilitator's signature to the fee-payer slot the agent left empty.
	Sign func(message []byte) ([]byte, error)
}

func New(pool *rpc.Pool, o Options) *Server {
	tp := o.TokenProgram
	if tp == "" {
		tp = solana.TokenProgramID.String()
	}
	return &Server{
		pool: pool, mint: o.Mint, payTo: o.PayTo, price: o.Price,
		feePayer: o.FeePayer, tokenProgram: tp, sign: o.Sign,
	}
}

func (s *Server) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/demo/search", s.handle)
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	return r
}

// handle is the whole x402 exchange, from the seller's side.
//
//	no payment  → 402, with what it would cost and where to send it
//	payment     → verify, submit, and answer 200 with a receipt
func (s *Server) handle(c *gin.Context) {
	// v2 first, then v1 — a client sends whichever its challenge declared.
	if h := c.GetHeader(challenge.HeaderPaymentV2); h != "" {
		s.settle(c, h, challenge.V2)
		return
	}
	if h := c.GetHeader(challenge.HeaderPaymentV1); h != "" {
		s.settle(c, h, challenge.V1)
		return
	}
	s.challenge(c)
}

// challenge issues the 402, in the shape pay-kit actually produces.
//
// Every field here is load-bearing for interoperability, and three would be easy to leave out:
//
//   - `protocol: "x402"`. pay-kit's Go client FILTERS offers on it. An offer without it is
//     silently ignored, and the agent hands its caller the raw 402 as though no payment were
//     possible. It is a pay-kit extension rather than upstream x402, which is exactly why it is
//     the thing most likely to be missed.
//   - `extra.feePayer`. This endpoint is its own facilitator, so it sponsors the network fee. An
//     agent must not pay its own — pay-kit refuses a transaction whose fee payer is also the
//     transfer authority.
//   - `extra.decimals` and `extra.tokenProgram`. The client needs both to build `transferChecked`
//     and to derive the recipient's token account. Omitting them makes it guess, and a guess about
//     Token-2022 derives an address that does not exist.
//
// Both `amount` and `maxAmountRequired` are emitted, and the network goes out as CAIP-2, because
// v2 is the default producer and this is what a v2 client expects to read.
func (s *Server) challenge(c *gin.Context) {
	resource := "http://" + c.Request.Host + c.Request.URL.Path

	offer := gin.H{
		"protocol": "x402",
		"scheme":   challenge.SchemeExact,
		"network":  challenge.WireNetwork(network.Sandbox, challenge.V2),
		// Base units, as a string. The protocol never carries a decimal here.
		"amount":            s.price.BaseUnits(),
		"maxAmountRequired": s.price.BaseUnits(),
		"asset":             s.mint,
		"payTo":             s.payTo,
		"maxTimeoutSeconds": 300,
		"extra": gin.H{
			"feePayer":     s.feePayer,
			"decimals":     6,
			"tokenProgram": s.tokenProgram,
		},
	}

	body := gin.H{
		"x402Version": int(challenge.V2),
		"error":       "payment_required",
		"resource":    c.Request.URL.Path,
		"accepts":     []gin.H{offer},
	}

	// The v2 challenge also travels as a header, standard padded base64 of the envelope. A client
	// reads the header before the body, so a server that only sets the body works but makes every
	// client do more work than it should.
	envelope, err := json.Marshal(gin.H{
		"x402Version": int(challenge.V2),
		"resource":    gin.H{"type": "http", "url": resource},
		"accepts":     []gin.H{offer},
	})
	if err == nil {
		c.Header(challenge.HeaderChallengeV2, base64.StdEncoding.EncodeToString(envelope))
	}

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusPaymentRequired, body)
}

// settle decodes the credential, co-signs as fee payer, and submits.
//
// The endpoint — not Leash — broadcasts. That is how x402 works, and it is why a Leash payment
// passes through `unknown`: we sign, somebody else submits, and we learn the outcome only by
// reading the chain back.
func (s *Server) settle(c *gin.Context, header string, version challenge.Version) {
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		// Standard PADDED base64. Accepting base64url here would let a malformed client through
		// and fail later, somewhere less obvious.
		s.refuse(c, "invalid_payment_header", "the payment header is not standard base64")
		return
	}

	var env challenge.PaymentEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		s.refuse(c, "invalid_payment_header", "the payment header is not a credential envelope")
		return
	}

	// A server MUST NOT silently accept a version it does not understand: it would settle a
	// payment whose wire contract it cannot read.
	if !challenge.VersionSupported(env.Version) {
		s.refuse(c, "version_mismatch",
			"unsupported x402 version "+strconv.Itoa(int(env.Version)))
		return
	}
	// v1 carries scheme and network beside the payload; v2 carries neither and binds `accepted`.
	if env.Version == challenge.V1 {
		if env.Scheme != challenge.SchemeExact {
			s.refuse(c, "invalid_payload_type", "v1 credentials must name the exact scheme")
			return
		}
		if cluster, ok := challenge.ClusterOf(env.Network); !ok || cluster != "devnet" {
			s.refuse(c, "network_mismatch", "this endpoint settles on the sandbox only")
			return
		}
	}
	if env.Payload.Transaction == "" {
		s.refuse(c, "invalid_payment_header", "the credential carries no transaction")
		return
	}

	txBytes, err := base64.StdEncoding.DecodeString(env.Payload.Transaction)
	if err != nil {
		s.refuse(c, "invalid_payment_header", "payload.transaction is not standard base64")
		return
	}
	tx, err := solana.TransactionFromBytes(txBytes)
	if err != nil {
		s.refuse(c, "invalid_payment_header", "payload.transaction is not a transaction")
		return
	}

	// The structural checks. This is where the real security lives, and it is ported from
	// pay-kit's verifier rather than invented — see verify.go.
	if err := s.verify(tx); err != nil {
		s.refuse(c, "invalid_exact_svm_payload_transaction", err.Error())
		return
	}

	// Co-sign the fee-payer slot the agent left empty. The agent signed as the transfer
	// AUTHORITY; this signature is what makes the transaction payable.
	if err := s.cosign(tx); err != nil {
		s.refuse(c, "settlement_failed", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	sig, err := rpc.Do(ctx, s.pool, network.Sandbox, func(cl *solrpc.Client) (solana.Signature, error) {
		return cl.SendTransactionWithOpts(ctx, tx, solrpc.TransactionOpts{
			SkipPreflight: false, PreflightCommitment: solrpc.CommitmentConfirmed,
		})
	})
	if err != nil {
		// The chain refused it. This is the hard tier working — an over-cap draw ends here, and
		// the agent is told plainly rather than being given a result it did not pay for.
		s.refuse(c, "send_failed", "the chain refused the payment: "+err.Error())
		return
	}

	responseHeader := challenge.HeaderResponseV2
	wireNet := challenge.WireNetwork(network.Sandbox, challenge.V2)
	if version == challenge.V1 {
		responseHeader = challenge.HeaderResponseV1
		wireNet = challenge.WireNetwork(network.Sandbox, challenge.V1)
	}
	settlement, _ := json.Marshal(challenge.SettlementResponse{
		Success: true, Transaction: sig.String(), Network: wireNet,
		Payer: tx.Message.AccountKeys[0].String(),
	})
	c.Header(responseHeader, base64.StdEncoding.EncodeToString(settlement))
	// pay-kit also emits the bare signature on its own header. It is an extension rather than
	// upstream x402, and clients that look for it should find it.
	c.Header("x-payment-settlement-signature", sig.String())

	c.JSON(http.StatusOK, gin.H{
		"results": []gin.H{
			{"title": "Solana", "url": "https://solana.com",
				"snippet": "A high-performance blockchain supporting builders around the world."},
			{"title": "x402", "url": "https://x402.org",
				"snippet": "An open standard for paying for HTTP resources per request."},
		},
		"receipt": gin.H{
			"signature": sig.String(), "amount": s.price.BaseUnits(), "pay_to": s.payTo,
		},
	})
}

// cosign fills the fee-payer signature slot the agent left zeroed.
func (s *Server) cosign(tx *solana.Transaction) error {
	if s.sign == nil {
		return errors.New("this endpoint has no facilitator key configured")
	}
	msg, err := tx.Message.MarshalBinary()
	if err != nil {
		return err
	}
	sigBytes, err := s.sign(msg)
	if err != nil {
		return err
	}
	payer := tx.Message.AccountKeys[0]
	fp, err := solana.PublicKeyFromBase58(s.feePayer)
	if err != nil {
		return err
	}
	if !payer.Equals(fp) {
		return errors.New("the transaction does not name this endpoint as its fee payer")
	}
	var sig solana.Signature
	copy(sig[:], sigBytes)
	if len(tx.Signatures) == 0 {
		return errors.New("the transaction carries no signature slots")
	}
	tx.Signatures[0] = sig
	return nil
}

// refuse answers with the error CODES pay-kit uses, so a client can branch on them.
func (s *Server) refuse(c *gin.Context, code, message string) {
	c.JSON(http.StatusPaymentRequired, gin.H{
		"error": gin.H{"code": code, "message": message, "retriable": false},
	})
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
