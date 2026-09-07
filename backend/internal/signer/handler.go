package signer

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/stone7890/leash/internal/domain/fault"
	"github.com/stone7890/leash/internal/signer/hot"
)

// Router is the signer's whole HTTP surface: one endpoint.
//
// No debug routes, no profiler, no static files. This process holds key material, and an image is
// a place where "just for debugging" becomes permanent.
func Router(s *hot.Signer, prov *Provisioner, health func() map[string]any) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), traceID())

	r.POST("/v1/sign", handleSign(s))

	// Internal only. nginx exposes /v1/sign and nothing else of this process, and these
	// additionally require a shared token — see provision.go.
	if prov != nil {
		prov.register(r)
	}

	// Liveness only. It says nothing about agents, keys or balances.
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, health()) })
	return r
}

// The signer takes the 402 body EXACTLY as the endpoint sent it.
//
// Passing it through unparsed means we read the same bytes the agent received, rather than a
// re-serialisation by the agent that could have dropped a field, reordered `extra`, or lost the
// version — any of which would change what we sign or what we refuse.
type signBody struct {
	Challenge json.RawMessage `json:"challenge"`
	Host      string          `json:"host"`
}

func handleSign(s *hot.Signer) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Authentication is the FIRST statement, not a middleware. A middleware you forgot to
		// attach fails open; a first line you forgot to write fails the build, because
		// verify-architecture asserts this shape on the AST.
		apiKey, err := requireAgentKey(c)
		if err != nil {
			fail(c, err)
			return
		}

		var body signBody
		if err := c.ShouldBindJSON(&body); err != nil {
			fail(c, fault.MalformedRequest.Wrapping(err))
			return
		}

		if len(body.Challenge) == 0 {
			fail(c, fault.MalformedRequest.WithMessage("the request carried no challenge"))
			return
		}

		// An x402 challenge carries no nonce, so two purchases of the same resource are
		// byte-identical. The agent is the only party that knows whether this is a retry of one it
		// already sent or a new purchase, so it must say — the same discipline every creating POST
		// in this API follows.
		attempt := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if attempt == "" {
			fail(c, fault.MissingIdempotencyKey.WithMessage(
				"an Idempotency-Key header is required: x402 challenges carry no nonce, so only "+
					"you can tell a retry from a new payment"))
			return
		}

		resp, err := s.Sign(c.Request.Context(), hot.Request{
			APIKey:         apiKey,
			Host:           body.Host,
			Challenge:      body.Challenge,
			IdempotencyKey: attempt,
			TraceID:        c.GetString("trace_id"),
		})
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(http.StatusOK, resp)
	}
}

// requireAgentKey reads the agent's API key.
//
// It does not verify it here — the lookup by hash IS the verification, and it happens inside the
// signing path where the snapshot is fetched. What this does is refuse a request that carries no
// credential at all, before any work is done.
func requireAgentKey(c *gin.Context) (string, error) {
	key := strings.TrimSpace(c.GetHeader("X-Leash-Key"))
	if key == "" {
		if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
			key = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		}
	}
	if key == "" {
		return "", fault.Unauthenticated.WithMessage("an agent API key is required")
	}
	if !strings.HasPrefix(key, "lk_test_") && !strings.HasPrefix(key, "lk_live_") {
		// S0's first half, refused before anything is read. A key without a network prefix cannot
		// be checked against an agent's network.
		return "", fault.NetworkMismatch.WithMessage(
			"an API key must begin with lk_test_ or lk_live_")
	}
	return key, nil
}

// fail renders any fault into the one envelope every endpoint uses.
func fail(c *gin.Context, err error) {
	var f fault.Fault
	if !errors.As(err, &f) {
		// An untyped error escaping a handler is a bug. It is logged with the real cause and
		// answered with a code that says nothing about internals.
		c.Error(err) //nolint:errcheck
		f = fault.Internal
	}
	body := fault.Body{
		Code: f.Code(), Message: f.Error(), Field: f.Field(),
		Details: f.Details(), Retriable: f.Retriable(),
		TraceID: c.GetString("trace_id"),
	}
	c.AbortWithStatusJSON(f.Status(), fault.Envelope{Error: body})
}
