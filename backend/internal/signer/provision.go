package signer

import (
	"context"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gin-gonic/gin"
	"github.com/stone7890/leash/internal/domain/fault"
	"github.com/stone7890/leash/internal/domain/ids"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/kms"
	"github.com/stone7890/leash/internal/store"
)

// Agent key provisioning.
//
// This lives in the signer because the signer is the ONLY holder of keys — that is the deck's
// wording and this repository's most-enforced rule. The dashboard creates the agent record; it
// cannot mint the keypair, because nothing under frontend/website may import a KMS client at all.
//
// The endpoint is INTERNAL. nginx exposes exactly one signer path, /v1/sign, so this is reachable
// only from inside the compose network, and it additionally requires a shared token. Both, not
// either: a network boundary that is one misconfigured proxy away from being public is not a
// boundary.

type Provisioner struct {
	store   *store.Store
	wrapper kms.Wrapper
	token   string
}

func NewProvisioner(st *store.Store, w kms.Wrapper, token string) *Provisioner {
	return &Provisioner{store: st, wrapper: w, token: token}
}

type provisionRequest struct {
	AgentID string `json:"agent_id"`
	Network string `json:"network"`
}

type provisionResponse struct {
	Pubkey string `json:"pubkey"`
	// APIKey is returned ONCE and never stored in a recoverable form. What the database holds is a
	// SHA-256 of it, so a disclosure hands nobody a working credential.
	APIKey string `json:"api_key"`
}

func (p *Provisioner) register(r *gin.Engine) {
	r.POST("/internal/agents/keys", p.handle)
	r.POST("/internal/agents/keys/rotate", p.rotate)
}

func (p *Provisioner) handle(c *gin.Context) {
	if err := p.requireInternal(c); err != nil {
		fail(c, err)
		return
	}

	var body provisionRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, fault.MalformedRequest.Wrapping(err))
		return
	}
	id, err := ids.ParseKind(body.AgentID, ids.KindAgent)
	if err != nil {
		fail(c, fault.MalformedRequest.WithMessage("not an agent identifier"))
		return
	}
	net, err := network.Parse(body.Network)
	if err != nil {
		fail(c, fault.InvalidNetwork)
		return
	}
	// The identifier carries its own network, and it must agree with what was asked for. This is
	// I8 at the one place a caller could otherwise pair a sandbox agent with a mainnet key.
	if idNet, _ := id.Network(); idNet != net {
		fail(c, fault.NetworkMismatch.WithMessage(
			"the agent identifier says "+idNet.String()+" but the request says "+net.String()))
		return
	}

	pub, apiKey, err := p.mint(c.Request.Context(), id.String(), net)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, provisionResponse{Pubkey: pub, APIKey: apiKey})
}

// rotate replaces an agent's key. The old one stops working the moment this returns — there is no
// grace period, because a rotation is usually a response to a suspected leak and a grace window
// keeps the leaked key alive through exactly the minutes that matter.
func (p *Provisioner) rotate(c *gin.Context) {
	if err := p.requireInternal(c); err != nil {
		fail(c, err)
		return
	}
	var body provisionRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, fault.MalformedRequest.Wrapping(err))
		return
	}
	id, err := ids.ParseKind(body.AgentID, ids.KindAgent)
	if err != nil {
		fail(c, fault.MalformedRequest.WithMessage("not an agent identifier"))
		return
	}
	net, _ := id.Network()

	seed, pub, err := kms.Generate()
	if err != nil {
		fail(c, err)
		return
	}
	wrapped, keyRef, err := p.wrapper.Wrap(seed)
	if err != nil {
		fail(c, err)
		return
	}
	apiKey, keyHash, err := kms.APIKey(net.KeyPrefix())
	if err != nil {
		fail(c, err)
		return
	}
	if err := p.store.RotateKey(c.Request.Context(), id.String(), wrapped, keyRef, keyHash); err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, provisionResponse{
		Pubkey: solana.PublicKeyFromBytes(pub).String(), APIKey: apiKey,
	})
}

func (p *Provisioner) mint(ctx context.Context, agentID string, net network.Network) (
	string, string, error) {
	seed, pub, err := kms.Generate()
	if err != nil {
		return "", "", err
	}
	wrapped, keyRef, err := p.wrapper.Wrap(seed)
	if err != nil {
		return "", "", err
	}
	// The key prefix comes from the agent's network, so a key physically cannot claim to belong to
	// the other one.
	apiKey, keyHash, err := kms.APIKey(net.KeyPrefix())
	if err != nil {
		return "", "", err
	}
	if err := p.store.PutAgentKey(ctx, agentID, wrapped, keyRef, keyHash, time.Now().UTC()); err != nil {
		return "", "", err
	}
	return solana.PublicKeyFromBytes(pub).String(), apiKey, nil
}

// requireInternal is the first statement of both handlers above.
//
// Constant-time comparison: a token checked with == leaks its prefix through timing, and this one
// guards the ability to mint agent keys.
func (p *Provisioner) requireInternal(c *gin.Context) error {
	if p.token == "" {
		return fault.Forbidden.WithMessage("internal provisioning is not configured")
	}
	got := c.GetHeader("X-Leash-Internal")
	if subtle.ConstantTimeCompare([]byte(got), []byte(p.token)) != 1 {
		return fault.Unauthenticated.WithMessage("internal token required")
	}
	return nil
}
