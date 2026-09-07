package indexer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gagliardetto/solana-go"
	solrpc "github.com/gagliardetto/solana-go/rpc"
	"github.com/gin-gonic/gin"
	"github.com/stone7890/leash/internal/chain/rpc"
	"github.com/stone7890/leash/internal/domain/challenge"
	"github.com/stone7890/leash/internal/domain/fault"
	"github.com/stone7890/leash/internal/domain/network"
)

// The test payment behind onboarding step 5.
//
// The specification is emphatic that this is the most often botched step, and about why: the log
// the owner watches must be REAL. So this runs the actual P2 loop — call the sample endpoint, take
// the 402, forward the challenge to the signer, replay with the payment — as the agent would, and
// every line the wizard renders is a handshake row written by the components that did the work.
//
// A scripted animation here would be a demonstration that the product works, shown to a user for
// whom it might not.

func (ix *Indexer) runTestPayment(c *gin.Context) {
	if !ix.internalOK(c) {
		c.JSON(http.StatusUnauthorized, fault.Envelope{Error: fault.Body{
			Code: "UNAUTHENTICATED", Message: "internal token required", Retriable: false}})
		return
	}
	var body struct {
		APIKey  string `json:"api_key"`
		Network string `json:"network"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.APIKey == "" {
		c.JSON(http.StatusBadRequest, fault.Envelope{Error: fault.Body{
			Code: "MALFORMED_REQUEST", Message: "an agent API key is required", Retriable: false}})
		return
	}
	net, err := network.Parse(body.Network)
	if err != nil || net != network.Sandbox {
		// The sample endpoint is a sandbox fixture. Running a "test" payment on mainnet would
		// spend real money, which is a nonsensical request rather than a forbidden one.
		c.JSON(http.StatusUnprocessableEntity, fault.Envelope{Error: fault.Body{
			Code:    "MAINNET_ONLY_OPERATION",
			Message: "a test payment runs on the sandbox only", Retriable: false}})
		return
	}

	// 202: the loop runs in the background and the client watches the stream. Returning the
	// finished result here would mean holding the request open across a chain confirmation.
	paymentID, err := ix.startTestPayment(c.Request.Context(), body.APIKey)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, fault.Envelope{Error: fault.Body{
			Code: "INTERNAL", Message: err.Error(), Retriable: true}})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"payment_id": paymentID})
}

// startTestPayment performs the handshake far enough to have a payment identifier to stream, then
// finishes the rest in the background.
func (ix *Indexer) startTestPayment(ctx context.Context, apiKey string) (string, error) {
	challenge, err := ix.fetchChallenge(ctx)
	if err != nil {
		return "", err
	}

	// One attempt identifier per test payment. A retry of THIS call would reuse it and replay;
	// a fresh test payment gets a new one and is signed afresh.
	attempt, err := newAttemptID()
	if err != nil {
		return "", err
	}
	signed, err := ix.callSigner(ctx, apiKey, hostOf(ix.demoURL), attempt, challenge)
	if err != nil {
		return "", err
	}

	// The payment exists and has an identifier, so the wizard can start streaming immediately —
	// the first three phases are already written by the signer.
	go func() {
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
		defer cancel()
		if err := ix.replay(bg, signed.Header, signed.Payload); err != nil {
			slog.Error("the test payment could not be settled by the endpoint",
				"payment_id", signed.PaymentID, "err", err)
			return
		}
		// The sweep will confirm it on its own cadence, but a wizard should not wait five minutes
		// for a sandbox payment that has already landed.
		ix.nudge(bg, signed.PaymentID)
	}()

	return signed.PaymentID, nil
}

// fetchChallenge takes the 402 body VERBATIM.
//
// It is forwarded to the signer unparsed, so the signer reads the same bytes the endpoint sent
// rather than a re-serialisation that could have dropped a field, reordered `extra`, or lost the
// version — any of which would change what gets signed or what gets refused.
func (ix *Indexer) fetchChallenge(ctx context.Context) (json.RawMessage, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ix.demoURL+"/demo/search", nil)
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
		return nil, fmt.Errorf("the sample endpoint answered %d, not 402: %s",
			res.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.RawMessage(body), nil
}

type signerReply struct {
	PaymentID string `json:"payment_id"`
	Signature string `json:"signature"`
	// Payload is the credential header value; Header is the name to put it in. The signer decides
	// both, from the version the endpoint's own challenge declared.
	Payload string `json:"payload"`
	Header  string `json:"header"`
}

func (ix *Indexer) callSigner(ctx context.Context, apiKey, host, attempt string,
	ch json.RawMessage) (signerReply, error) {
	payload, _ := json.Marshal(map[string]any{"challenge": ch, "host": host})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		ix.signerURL+"/v1/sign", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Leash-Key", apiKey)
	req.Header.Set("Idempotency-Key", attempt)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return signerReply{}, fmt.Errorf("calling the signer: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		// A block here is a legitimate outcome, not a broken demo — and the message says which
		// rule refused, because that is what the owner needs in order to fix it.
		return signerReply{}, fmt.Errorf("the signer refused this payment: %s", strings.TrimSpace(string(raw)))
	}
	var out signerReply
	return out, json.Unmarshal(raw, &out)
}

func (ix *Indexer) replay(ctx context.Context, header, payload string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ix.demoURL+"/demo/search", nil)
	if header == "" {
		header = challenge.HeaderPaymentV2
	}
	// The header the SIGNER chose, which follows the version the endpoint's challenge declared.
	// Guessing here would put a v2 credential in the v1 header for a v1 server, which sees nothing.
	req.Header.Set(header, payload)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("the endpoint did not accept the payment (%d): %s",
			res.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// nudge confirms one payment quickly, so the onboarding wizard does not wait for the sweep.
//
// It uses the SAME lookup the sweep uses — `look`, which tries the signature and then the memo.
// That matters: a sponsored payment settles under the FEE PAYER's signature, so looking up the one
// we produced never resolves, and a wizard doing that would sit on its most important screen for
// five minutes before the sweep rescued it.
//
// It does not shortcut the read-back. A confirmation that skipped it would be a lie in the
// timeline, and this is the timeline a customer is watching for the first time.
func (ix *Indexer) nudge(ctx context.Context, paymentID string) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		p, err := ix.store.GetPayment(ctx, paymentID)
		if err != nil {
			return
		}
		if p.State == "confirmed" || p.State == "failed" {
			return
		}
		conf, err := ix.look(ctx, p.Network, p)
		if err == nil && conf.Confirmed && conf.Err == "" {
			if conf.Signature != "" && conf.Signature != p.Signature {
				_ = ix.store.SetSettledSignature(ctx, p.Network, p.ID, conf.Signature)
			}
			ix.confirm(ctx, p.Network, p, conf.Slot, time.Now().UTC())
			return
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return
		}
	}
}

// Submit broadcasts an owner-signed transaction and waits for it to confirm.
func (ix *Indexer) Submit(ctx context.Context, net network.Network, b64 string) (string, error) {
	tx, err := solana.TransactionFromBase64(b64)
	if err != nil {
		return "", fmt.Errorf("not a signed transaction: %w", err)
	}
	sig, err := rpc.Do(ctx, ix.pool, net, func(c *solrpc.Client) (solana.Signature, error) {
		return c.SendTransactionWithOpts(ctx, tx, solrpc.TransactionOpts{
			PreflightCommitment: solrpc.CommitmentConfirmed,
		})
	})
	if err != nil {
		return "", err
	}
	return sig.String(), nil
}

// newAttemptID identifies one payment attempt.
func newAttemptID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	}
	return u.Host
}
