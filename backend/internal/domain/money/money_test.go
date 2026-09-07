package money

import (
	"encoding/json"
	"testing"
)

func TestParseAcceptsWhatItShould(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Base
	}{
		{"1", One},
		{"1.0", One},
		{"1.000000", One},
		{"0.31", 310_000},
		{"0.310000", 310_000},
		{"0.000001", 1},
		{"10", 10 * One},
		{"7.42", 7_420_000},
		{"1000000000", Max},
	} {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q) errored: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// The permissiveness people expect from a number parser is what turns "1e3" into a payment
// nobody intended. Each of these is a real shape that a lenient parser would have accepted.
func TestParseRefusesEverythingElse(t *testing.T) {
	for _, in := range []string{
		"", ".", "1.", ".5", "-1", "+1", "1e3", "1E3", "0x10", " 1", "1 ",
		"1,000", "1.0000001", "$1", "1USDC", "NaN", "Infinity", "١",
	} {
		if got, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %d, want an error", in, got)
		}
	}
}

func TestZeroIsNotAPayment(t *testing.T) {
	if _, err := Parse("0"); err != ErrNotPositive {
		t.Errorf("Parse(\"0\") should be ErrNotPositive, got %v", err)
	}
	if v, err := ParseAllowingZero("0"); err != nil || v != 0 {
		t.Errorf("ParseAllowingZero(\"0\") = %d, %v", v, err)
	}
}

func TestStringAlwaysShowsSixPlaces(t *testing.T) {
	for _, c := range []struct {
		in   Base
		want string
	}{
		{One, "1.000000"},
		{310_000, "0.310000"},
		{1, "0.000001"},
		{0, "0.000000"},
		{7_420_000, "7.420000"},
		{Max, "1000000000.000000"},
	} {
		if got := c.in.String(); got != c.want {
			t.Errorf("Base(%d).String() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEveryValueRoundTrips(t *testing.T) {
	for _, v := range []Base{1, 999_999, One, 7_420_000, Max} {
		got, err := Parse(v.String())
		if err != nil || got != v {
			t.Errorf("round trip of %d gave %d, %v", v, got, err)
		}
	}
}

// I6, on the wire. A client sending a JSON number must be told, not quietly accommodated.
func TestJSONRefusesANumberAndEmitsAString(t *testing.T) {
	var b Base
	if err := json.Unmarshal([]byte(`0.31`), &b); err == nil {
		t.Error("a JSON number was accepted as an amount")
	}
	if err := json.Unmarshal([]byte(`"0.31"`), &b); err != nil || b != 310_000 {
		t.Errorf("a JSON string was refused: %d, %v", b, err)
	}
	out, _ := json.Marshal(Base(310_000))
	if string(out) != `"0.310000"` {
		t.Errorf("marshalled to %s, want a quoted string", out)
	}
}

// The values that prove the point.
//
// Each of these is a perfectly ordinary payment amount that a float-based parser gets wrong by
// one micro-USDC: float64("2.01") * 1e6 is 2009999.9999999998, and truncating that loses a unit.
// Accumulated across an audit trail, that is exactly the drift invariant I6 exists to prevent.
func TestAmountsAFloatParserWouldGetWrong(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Base
	}{
		{"2.010000", 2_010_000},
		{"2.030000", 2_030_000},
		{"2.070000", 2_070_000},
		{"4.020000", 4_020_000},
		{"8.070000", 8_070_000},
	} {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %d, want %d — a float crept in", c.in, got, c.want)
		}
		if rt := got.String(); rt != c.in {
			t.Errorf("round trip of %q gave %q", c.in, rt)
		}
	}
}

// The largest amount the schema permits must survive parsing and formatting intact.
func TestTheMaximumRoundTrips(t *testing.T) {
	if got, err := Parse("1000000000.000000"); err != nil || got != Max {
		t.Errorf("Parse(max) = %d, %v", got, err)
	}
	if got, err := Parse("1000000000.000001"); err == nil {
		t.Errorf("Parse(max+1) = %d, want ErrTooLarge", got)
	}
}

func TestRemainingClampsAtZero(t *testing.T) {
	if got := Remaining(10*One, 2*One, One); got != 7*One {
		t.Errorf("Remaining = %d, want %d", got, 7*One)
	}
	// drawn + reserved past the cap should never reach here — the database refuses such a
	// document — but if it does, a negative remaining must not be compared against an amount.
	if got := Remaining(10*One, 9*One, 5*One); got != 0 {
		t.Errorf("Remaining should clamp to 0, got %d", got)
	}
}

// drawn, reserved and remaining are legitimately zero, in any spelling. Refusing zero at the
// encoding layer would make a correct response unrepresentable.
func TestZeroDecodesFromTheWireInEverySpelling(t *testing.T) {
	for _, s := range []string{"0", "0.0", "0.000000"} {
		v, err := ParseAllowingZero(s)
		if err != nil || v != 0 {
			t.Errorf("ParseAllowingZero(%q) = %d, %v", s, v, err)
		}
	}
	var b Base
	if err := json.Unmarshal([]byte(`"0.000000"`), &b); err != nil || b != 0 {
		t.Errorf("unmarshalling a zero amount failed: %d, %v", b, err)
	}
	// A malformed zero is still malformed.
	if _, err := ParseAllowingZero("0.0.0"); err == nil {
		t.Error("a malformed zero was accepted")
	}
}
