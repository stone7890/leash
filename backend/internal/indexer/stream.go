package indexer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stone7890/leash/internal/domain/state"
	"github.com/stone7890/leash/internal/store"
)

// Router serves the onboarding stream, and nothing else.
//
// This process exists because its work is loops, not requests. The one endpoint it binds is here
// because a resumable change stream held open for a wizard session cannot live in an
// invocation-scoped runtime.
func (ix *Indexer) Router(health func() map[string]any) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/v1/test-payments/:id/stream", ix.stream)

	// Internal only: transaction building, submission, and the onboarding test payment. nginx
	// exposes the stream above and nothing else of this process, and these additionally require
	// the shared token.
	r.POST("/internal/intents", ix.buildIntent)
	r.POST("/internal/submit", ix.submitSigned)
	r.POST("/internal/test-payments", ix.runTestPayment)
	r.POST("/internal/faucet", ix.faucet)
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, health()) })
	return r
}

// stream sends the handshake as it happens.
//
// The specification is emphatic that onboarding step 5 is the most often botched step: the log the
// owner watches must be REAL — one line per handshake row, streamed from the server as the payment
// actually happens. A scripted animation in the frontend would be a demonstration that the product
// works, shown to a user for whom it might not.
//
// So this replays what has already been recorded, then follows the change stream for the rest.
// Replaying first matters: a client that connects a moment late must not miss the beginning.
func (ix *Indexer) stream(c *gin.Context) {
	paymentID := c.Param("id")

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	// nginx buffers proxied responses by default, which would deliver the whole log in one lump at
	// the end and defeat the entire point. The location block turns buffering off; this says so
	// again for any proxy that reads the header instead.
	c.Header("X-Accel-Buffering", "no")

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()

	seen := map[state.Phase]bool{}
	send := func(e store.HandshakeEvent) bool {
		if seen[e.Phase] {
			return true
		}
		seen[e.Phase] = true
		payload, err := json.Marshal(map[string]any{
			"phase":          string(e.Phase),
			"writer":         string(e.Writer),
			"at":             e.At,
			"detail":         e.Detail,
			"read_back_slot": e.ReadBackSlot,
		})
		if err != nil {
			return true
		}
		ok := true
		c.Stream(func(w io.Writer) bool {
			if _, err := w.Write([]byte("event: phase\ndata: " + string(payload) + "\n\n")); err != nil {
				ok = false
			}
			return false
		})
		return ok
	}

	// Everything already recorded, in order.
	existing, err := ix.store.Timeline(ctx, paymentID)
	if err == nil {
		for _, e := range existing {
			if !send(e) {
				return
			}
		}
		if seen[state.PhaseConfirmed] {
			c.Stream(func(w io.Writer) bool {
				_, _ = w.Write([]byte("event: done\ndata: {}\n\n"))
				return false
			})
			return
		}
	}

	// Then the rest, as it happens.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ix.store.WatchHandshake(ctx, paymentID, func(e store.HandshakeEvent) {
			send(e)
			if e.Phase == state.PhaseConfirmed {
				cancel()
			}
		})
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
	c.Stream(func(w io.Writer) bool {
		_, _ = w.Write([]byte("event: done\ndata: {}\n\n"))
		return false
	})
}
