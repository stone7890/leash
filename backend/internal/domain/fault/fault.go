// Package fault is every error that reaches a client, and the single envelope they become.
//
// A code is a stable machine-readable identifier that NEVER changes. Renaming one breaks the agent
// SDK's branching, the interface's copy table, and any operator runbook that maps a code to an
// action. contracts/error-codes.json is the frozen register, and a test in this package asserts
// that what is defined here and what is in that file are the same set — so adding a code is a
// deliberate act in two places rather than an accident in one.
//
// `message` is for a person and may be reworded or translated; it is never parsed. If you find
// yourself wanting to parse one, the information belongs in `details`.
package fault

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Fault is what every layer's error type implements, so the transport can render any of them
// without a type switch that has to be kept in sync.
type Fault interface {
	error
	Code() string    // stable; never renamed
	Status() int     // the HTTP status this maps to
	Field() string   // the input to blame, or "" when there is none
	Retriable() bool // always transmitted, even when false
	Details() any    // nil when there is nothing structured to add
}

// Body is the inside of the envelope.
//
// Note what carries `omitempty` and what does not. `Retriable` deliberately does not: its zero
// value is meaningful, and an SDK must not have to infer retriability from the code. Everything
// optional is OMITTED when absent rather than sent as null.
type Body struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Field     string `json:"field,omitempty"`
	Details   any    `json:"details,omitempty"`
	Retriable bool   `json:"retriable"`
	TraceID   string `json:"trace_id,omitempty"`
}

// Envelope is the whole response body on an error path, from every endpoint of every runtime.
type Envelope struct {
	Error Body `json:"error"`
}

// E is the concrete fault. Layers construct one through the helpers below rather than filling it
// in by hand, so that a code cannot be paired with a status it was not registered with.
type E struct {
	code      string
	message   string
	status    int
	field     string
	retriable bool
	details   any
	wrapped   error
}

func (e *E) Error() string {
	if e.message != "" {
		return e.message
	}
	return e.code
}
func (e *E) Code() string    { return e.code }
func (e *E) Status() int     { return e.status }
func (e *E) Field() string   { return e.field }
func (e *E) Retriable() bool { return e.retriable }
func (e *E) Details() any    { return e.details }
func (e *E) Unwrap() error   { return e.wrapped }

// WithField names the input to blame, so the interface can highlight it rather than showing a
// generic toast.
func (e *E) WithField(f string) *E { c := *e; c.field = f; return &c }

// WithDetails attaches structured context. On a policy block this carries the per-rule results,
// which the interface renders as phase 2 of the timeline.
func (e *E) WithDetails(d any) *E { c := *e; c.details = d; return &c }

// WithMessage replaces the human sentence. The code is untouched.
func (e *E) WithMessage(m string) *E { c := *e; c.message = m; return &c }

// Wrapping keeps the underlying cause for the log without putting it on the wire.
func (e *E) Wrapping(err error) *E { c := *e; c.wrapped = err; return &c }

// Envelope renders the fault. traceID joins the response, the log line and the handshake detail,
// so a support ticket, a log and a chain event can be joined on one value.
func (e *E) Envelope(traceID string) Envelope {
	return Envelope{Error: Body{
		Code:      e.code,
		Message:   e.Error(),
		Field:     e.field,
		Details:   e.details,
		Retriable: e.retriable,
		TraceID:   traceID,
	}}
}

func (e *E) MarshalJSON() ([]byte, error) { return json.Marshal(e.Envelope("")) }

// As extracts a Fault from anywhere in an error chain.
func As(err error) (Fault, bool) {
	var f Fault
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}

// Is compares by code, which is the only identity an error has across a process boundary.
func Is(err error, code string) bool {
	f, ok := As(err)
	return ok && f.Code() == code
}

func def(code string, status int, retriable bool, message string) *E {
	return &E{code: code, status: status, retriable: retriable, message: message}
}

// ── Policy verdicts · 403 ────────────────────────────────────────────────────
//
// These are not errors in the sense of something having gone wrong. A block is the product
// working, and the agent SDK branches on exactly these eight.

var (
	NetworkMismatch    = def("NETWORK_MISMATCH", http.StatusForbidden, false, "the key's network does not match the agent's")
	AllowanceInactive  = def("ALLOWANCE_INACTIVE", http.StatusForbidden, false, "the allowance is not active")
	EndpointNotAllowed = def("ENDPOINT_NOT_ALLOWED", http.StatusForbidden, false, "the endpoint is not on this agent's allow list")
	PerTxLimit         = def("PER_TX_LIMIT", http.StatusForbidden, false, "the amount exceeds this agent's per-payment maximum")
	// The only retriable block: a velocity window clears on its own, so an SDK may back off and
	// try again. Every other block needs a person to change something.
	VelocityLimit    = def("VELOCITY_LIMIT", http.StatusForbidden, true, "this agent's ten-minute limit is full")
	BudgetExhausted  = def("BUDGET_EXHAUSTED", http.StatusForbidden, false, "the remaining budget is smaller than this payment")
	UnsupportedTerms = def("UNSUPPORTED_TERMS", http.StatusForbidden, false, "the payment terms are not supported")
	Killed           = def("KILLED", http.StatusForbidden, false, "this agent has been stopped")
)

// ── Validation · 400 and 422 ─────────────────────────────────────────────────

var (
	InvalidAmount  = def("INVALID_AMOUNT", http.StatusUnprocessableEntity, false, "amount must be a decimal string with at most six places")
	InvalidHost    = def("INVALID_HOST", http.StatusUnprocessableEntity, false, "not a valid hostname")
	InvalidWallet  = def("INVALID_WALLET", http.StatusUnprocessableEntity, false, "not a valid Solana address")
	InvalidNetwork = def("INVALID_NETWORK", http.StatusUnprocessableEntity, false, "network must be sandbox or mainnet")
	// Refusing an unknown challenge version is deliberate. Canonicalising it by guesswork would
	// produce a different hash for the same challenge, which defeats I7 and permits a double
	// payment. Refusing is the safe direction.
	UnsupportedChallengeVersion = def("UNSUPPORTED_CHALLENGE_VERSION", http.StatusUnprocessableEntity, false, "this challenge version is not recognised")
	TooManyHosts                = def("TOO_MANY_HOSTS", http.StatusUnprocessableEntity, false, "exactly one host may be added, and it may not be a wildcard or a parent domain")
	MainnetOnlyOperation        = def("MAINNET_ONLY_OPERATION", http.StatusUnprocessableEntity, false, "this operation does not apply on this network")
	MissingIdempotencyKey       = def("MISSING_IDEMPOTENCY_KEY", http.StatusBadRequest, false, "an Idempotency-Key header is required")
	MalformedRequest            = def("MALFORMED_REQUEST", http.StatusBadRequest, false, "the request could not be read")
)

// ── Conflict · 409 ───────────────────────────────────────────────────────────

var (
	IdempotencyConflict = def("IDEMPOTENCY_CONFLICT", http.StatusConflict, false, "that Idempotency-Key was used with a different request")
	// Retry with the SAME challenge. Generating a fresh one creates a second payment for one API
	// call, which is the exact failure I7 exists to prevent.
	SignInProgress       = def("SIGN_IN_PROGRESS", http.StatusConflict, true, "this challenge is already being signed — retry with the same challenge")
	AllowanceAlreadyLive = def("ALLOWANCE_ALREADY_LIVE", http.StatusConflict, false, "this agent already has a live allowance for that mint")
	IntentExpired        = def("INTENT_EXPIRED", http.StatusConflict, true, "the transaction expired before it was signed — request a new one")
)

// ── Authentication · 401 and 403 ─────────────────────────────────────────────
//
// Every sign-in failure returns one identical 401 to the client. The real reason is always logged.

var (
	Unauthenticated  = def("UNAUTHENTICATED", http.StatusUnauthorized, false, "sign in to continue")
	InvalidSignature = def("INVALID_SIGNATURE", http.StatusUnauthorized, false, "sign in to continue")
	NonceAlreadyUsed = def("NONCE_ALREADY_USED", http.StatusUnauthorized, false, "sign in to continue")
	NonceExpired     = def("NONCE_EXPIRED", http.StatusUnauthorized, false, "sign in to continue")
	KeyRevoked       = def("KEY_REVOKED", http.StatusUnauthorized, false, "this API key is no longer valid")
	Forbidden        = def("FORBIDDEN", http.StatusForbidden, false, "not permitted")
)

// ── Storage, dependencies, and the catch-all ─────────────────────────────────

var (
	// A resource belonging to another organisation returns this, never 403 — a 403 confirms the
	// identifier exists, which is an enumeration oracle.
	NotFound         = def("NOT_FOUND", http.StatusNotFound, false, "not found")
	RateLimited      = def("RATE_LIMITED", http.StatusTooManyRequests, true, "too many requests")
	ChainUnreachable = def("CHAIN_UNREACHABLE", http.StatusServiceUnavailable, true, "the chain is temporarily unreachable")
	StoreUnavailable = def("STORE_UNAVAILABLE", http.StatusServiceUnavailable, true, "temporarily unavailable")
	// Deliberately says nothing about internals. The real error goes to the log with the trace.
	Internal = def("INTERNAL", http.StatusInternalServerError, false, "something went wrong")
)

// All is every fault the wire can carry, and it is what the contract test walks.
func All() []*E {
	return []*E{
		NetworkMismatch, AllowanceInactive, EndpointNotAllowed, PerTxLimit,
		VelocityLimit, BudgetExhausted, UnsupportedTerms, Killed,
		InvalidAmount, InvalidHost, InvalidWallet, InvalidNetwork,
		UnsupportedChallengeVersion, TooManyHosts, MainnetOnlyOperation,
		MissingIdempotencyKey, MalformedRequest,
		IdempotencyConflict, SignInProgress, AllowanceAlreadyLive, IntentExpired,
		Unauthenticated, InvalidSignature, NonceAlreadyUsed, NonceExpired, KeyRevoked, Forbidden,
		NotFound, RateLimited, ChainUnreachable, StoreUnavailable, Internal,
	}
}
