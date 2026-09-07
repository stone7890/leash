package money

import "errors"

// MarshalJSON always emits a STRING.
//
// JSON numbers are IEEE doubles. A cap of 10.000000 survives one, but a balance of
// 9007199254740993 micro-USDC does not, and neither does any arithmetic a client performs on it.
// Emitting a string means a client that wants a number has to make that mistake deliberately.
func (b Base) MarshalJSON() ([]byte, error) {
	s := b.String()
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	out = append(out, s...)
	out = append(out, '"')
	return out, nil
}

var errNotAString = errors.New(
	"amount must be a JSON string, not a number — JSON numbers are IEEE doubles")

// UnmarshalJSON refuses a JSON number rather than accepting it.
//
// This is the half people forget. Emitting strings while quietly accepting numbers means a client
// can round-trip a value through a double and we will store the result as though it were exact.
// {"amount":"0.31"} works; {"amount":0.31} is INVALID_AMOUNT, which is the correct education.
//
// Zero is accepted here, because `drawn`, `reserved` and `remaining` are legitimately zero on the
// wire. Positivity is not a property of the encoding — it belongs to the fields that require it,
// and it is asserted by New and Parse at the point where a zero would be a malformed payment.
func (b *Base) UnmarshalJSON(p []byte) error {
	if len(p) < 2 || p[0] != '"' || p[len(p)-1] != '"' {
		return errNotAString
	}
	v, err := ParseAllowingZero(string(p[1 : len(p)-1]))
	if err != nil {
		return err
	}
	*b = v
	return nil
}
