package signer

import (
	"github.com/gin-gonic/gin"
	"github.com/stone7890/leash/internal/domain/ids"
)

// traceID gives every request an identifier that joins the response envelope, the log line and the
// handshake record. Give it to support and they can find the request.
//
// This is not authentication and does not pretend to be. Authentication is an explicit first
// statement in each handler.
func traceID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-Id")
		if id == "" {
			if v, err := ids.New(ids.KindJob); err == nil {
				id = v.ULID()
			}
		}
		c.Set("trace_id", id)
		c.Header("X-Request-Id", id)
		c.Next()
	}
}
