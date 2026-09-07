// Package money is the only place an amount is defined.
//
// Every amount in Leash is an integer count of micro-USDC. USDC has six decimal places, so one
// dollar is 1,000,000 base units.
//
// There is no floating point in this package and there must not be one anywhere in a money path.
// `long` in MongoDB, int64 here, u64 in the protocol, and a STRING on the wire — JSON numbers are
// IEEE doubles, and a double is exactly how an audit trail stops adding up. That is invariant I6,
// and `leashctl verify-architecture` fails the build on float64, ParseFloat, or a map[string]any
// decode anywhere that touches money.
package money

import (
	"errors"
	"strings"
)

// Base is an amount in micro-USDC.
type Base int64

const (
	// Decimals is USDC's precision. Not configurable: a mint with different precision is a
	// different product decision, not a constant to loosen.
	Decimals = 6

	// One is one USDC.
	One = Base(1_000_000)

	// Max is $1,000,000,000 expressed in micro-USDC.
	//
	// It matches the `maximum` in every money field's $jsonSchema, and it exists for a reason
	// beyond sanity: the signer admits payments with an aggregation that sums amounts, and
	// MongoDB's $sum promotes to a double on overflow. Capping every individual amount well
	// below 2^63 keeps every sum the signer can construct inside the integer range, so the
	// promotion never happens and the comparison is never made in floating point.
	Max = Base(1_000_000_000_000_000)
)

var (
	ErrNotPositive     = errors.New("amount must be greater than zero")
	ErrTooLarge        = errors.New("amount exceeds the maximum")
	ErrMalformed       = errors.New("amount is not a decimal string")
	ErrTooPrecise      = errors.New("amount has more than six decimal places")
	ErrStoredAsDouble  = errors.New("amount was stored as a double — money has been lost")
	ErrStoredAsInt32   = errors.New("amount was stored as an int32 — a raw write bypassed the schema")
	ErrStoredAsDecimal = errors.New("amount was stored as a decimal128 — a raw write bypassed the schema")
)

// New builds a positive amount from base units.
func New(v int64) (Base, error) {
	if v <= 0 {
		return 0, ErrNotPositive
	}
	if Base(v) > Max {
		return 0, ErrTooLarge
	}
	return Base(v), nil
}

// NewAllowingZero builds an amount that may be zero, for counters like `drawn` where zero is a
// legitimate starting value rather than a malformed input.
func NewAllowingZero(v int64) (Base, error) {
	if v < 0 {
		return 0, ErrNotPositive
	}
	if Base(v) > Max {
		return 0, ErrTooLarge
	}
	return Base(v), nil
}

// Parse reads the decimal string the API and the interface use.
//
// At most six decimal places, no exponent, no sign, no unit suffix, no separators. The
// permissiveness people expect from a number parser is exactly what turns "1e3" into a payment
// nobody intended, so this parser refuses everything it was not asked for.
func Parse(s string) (Base, error) {
	if s == "" {
		return 0, ErrMalformed
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" || (hasDot && frac == "") {
		return 0, ErrMalformed
	}
	if len(frac) > Decimals {
		return 0, ErrTooPrecise
	}
	// Right-pad the fraction so "0.31" and "0.310000" are the same number.
	frac += strings.Repeat("0", Decimals-len(frac))

	var n int64
	for _, part := range []string{whole, frac} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, ErrMalformed
			}
			// Overflow is checked against Max below, but guard the multiply itself so a long
			// input cannot wrap into a small positive number.
			if n > (int64(Max)+9)/10 {
				return 0, ErrTooLarge
			}
			n = n*10 + int64(r-'0')
		}
	}
	if n <= 0 {
		return 0, ErrNotPositive
	}
	if Base(n) > Max {
		return 0, ErrTooLarge
	}
	return Base(n), nil
}

// ParseAllowingZero is Parse, for the fields where zero is legitimate: `drawn`, `reserved` and
// `remaining` all start at or return to zero, and refusing it there would be wrong.
func ParseAllowingZero(s string) (Base, error) {
	v, err := Parse(s)
	if err == ErrNotPositive {
		return 0, nil // a well-formed zero, in any spelling Parse accepted before rejecting it
	}
	return v, err
}

// String always shows six places, so columns line up in a CSV and the format is unambiguous.
func (b Base) String() string {
	neg := b < 0
	v := int64(b)
	if neg {
		v = -v
	}
	whole := v / int64(One)
	frac := v % int64(One)

	var sb strings.Builder
	if neg {
		sb.WriteByte('-')
	}
	sb.WriteString(itoa(whole))
	sb.WriteByte('.')
	// Fixed six digits, zero-padded on the left.
	d := itoa(frac)
	sb.WriteString(strings.Repeat("0", Decimals-len(d)))
	sb.WriteString(d)
	return sb.String()
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// Add and Sub exist so arithmetic on money goes through a place that can be audited, rather than
// being scattered as bare + and - across the codebase.
func (b Base) Add(o Base) Base { return b + o }
func (b Base) Sub(o Base) Base { return b - o }

// Remaining is the budget arithmetic of docs/04-policy-engine.md, in one place:
//
//	remaining = cap − drawn − reserved
//
// It clamps at zero. A negative remaining is not a number to propagate — it means `drawn` and
// `reserved` have drifted past the cap, which the database's `drawn + reserved <= cap` validator
// should already have refused. Returning a negative here would let a caller compare it against an
// amount and reach a wrong conclusion quietly.
func Remaining(capBase, drawn, reserved Base) Base {
	r := capBase - drawn - reserved
	if r < 0 {
		return 0
	}
	return r
}

// ParseBaseUnits reads an integer count of base units, as the x402 wire carries amounts.
//
// The protocol says "1000" and means one thousandth of a six-decimal token. A decimal string here
// would be wrong by a factor of a million, silently and in the direction of overpaying — so this
// refuses anything with a decimal point rather than trying to be helpful about it.
func ParseBaseUnits(s string) (Base, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, ErrMalformed
	}
	if strings.ContainsAny(t, ".eE+-") {
		return 0, ErrMalformed
	}
	var n int64
	for _, r := range t {
		if r < '0' || r > '9' {
			return 0, ErrMalformed
		}
		if n > (int64(Max)+9)/10 {
			return 0, ErrTooLarge
		}
		n = n*10 + int64(r-'0')
	}
	if n <= 0 {
		return 0, ErrNotPositive
	}
	if Base(n) > Max {
		return 0, ErrTooLarge
	}
	return Base(n), nil
}

// BaseUnits renders an amount the way the x402 wire carries it.
func (b Base) BaseUnits() string { return itoa(int64(b)) }
