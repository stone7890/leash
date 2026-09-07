package fault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type contract struct {
	Codes []struct {
		Code      string `json:"code"`
		Status    int    `json:"status"`
		Retriable bool   `json:"retriable"`
		Rule      string `json:"rule"`
		Tier      string `json:"tier"`
		Group     string `json:"group"`
	} `json:"codes"`
}

func loadContract(t *testing.T) contract {
	t.Helper()
	p := filepath.Join("..", "..", "..", "..", "contracts", "error-codes.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	var c contract
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parsing %s: %v", p, err)
	}
	return c
}

// The frozen table. A code added in Go and not in the contract fails here, and so does the
// reverse — which is what makes adding one a deliberate act in two places rather than an accident
// in one. The TypeScript runtime is checked against the same file.
func TestTheCodeTableMatchesTheContract(t *testing.T) {
	c := loadContract(t)

	inGo := map[string]*E{}
	for _, e := range All() {
		if _, dup := inGo[e.Code()]; dup {
			t.Errorf("%s is defined twice — a code is never reused", e.Code())
		}
		inGo[e.Code()] = e
	}

	inContract := map[string]bool{}
	for _, row := range c.Codes {
		inContract[row.Code] = true
		e, ok := inGo[row.Code]
		if !ok {
			t.Errorf("%s is in the contract but not in Go", row.Code)
			continue
		}
		if e.Status() != row.Status {
			t.Errorf("%s: status is %d in Go, %d in the contract", row.Code, e.Status(), row.Status)
		}
		// Retriability is part of the contract: flipping it changes SDK behaviour, so it is
		// treated as seriously as a rename.
		if e.Retriable() != row.Retriable {
			t.Errorf("%s: retriable is %v in Go, %v in the contract", row.Code, e.Retriable(), row.Retriable)
		}
	}
	for code := range inGo {
		if !inContract[code] {
			t.Errorf("%s is in Go but not in the contract — add it to contracts/error-codes.json", code)
		}
	}
}

// Codes are an interface. Screaming snake case, and nothing else, so they are unmistakable in a
// log and stable in a switch.
func TestEveryCodeIsScreamingSnakeCase(t *testing.T) {
	for _, e := range All() {
		for _, r := range e.Code() {
			if !(r >= 'A' && r <= 'Z') && r != '_' {
				t.Errorf("%q is not screaming snake case", e.Code())
				break
			}
		}
	}
}

func TestEnvelopeHasASingleErrorKeyAndOmitsAbsentMembers(t *testing.T) {
	b, err := json.Marshal(EndpointNotAllowed.Envelope(""))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)

	if !strings.HasPrefix(s, `{"error":{`) {
		t.Errorf("body does not open with a single error key: %s", s)
	}
	// Absent optional members are OMITTED, never null. A client that sees "field" knows there is
	// an input to blame; one that sees null has to decide what null means.
	for _, absent := range []string{`"field"`, `"details"`, `"trace_id"`, "null"} {
		if strings.Contains(s, absent) {
			t.Errorf("%s should be absent from %s", absent, s)
		}
	}
	// retriable is present even when false — an SDK must not infer it from the code.
	if !strings.Contains(s, `"retriable":false`) {
		t.Errorf("retriable:false must be transmitted, got %s", s)
	}
}

func TestOptionalMembersAppearWhenSet(t *testing.T) {
	e := EndpointNotAllowed.
		WithField("host").
		WithDetails(map[string]string{"rule": "S2"})
	b, _ := json.Marshal(e.Envelope("01J8XKQ2M4Z7YB3F9C5R6D8W1H"))
	s := string(b)
	for _, want := range []string{`"field":"host"`, `"rule":"S2"`, `"trace_id":"01J8`} {
		if !strings.Contains(s, want) {
			t.Errorf("%s missing from %s", want, s)
		}
	}
}

// The builders must not mutate the package-level fault, or one request's field would leak into
// every subsequent response carrying the same code.
func TestBuildersDoNotMutateTheSharedFault(t *testing.T) {
	_ = EndpointNotAllowed.WithField("host").WithDetails("x").WithMessage("y")
	if EndpointNotAllowed.Field() != "" {
		t.Error("WithField mutated the shared value")
	}
	if EndpointNotAllowed.Details() != nil {
		t.Error("WithDetails mutated the shared value")
	}
	if EndpointNotAllowed.Error() == "y" {
		t.Error("WithMessage mutated the shared value")
	}
}

func TestVelocityIsTheOnlyRetriablePolicyBlock(t *testing.T) {
	policy := []*E{NetworkMismatch, AllowanceInactive, EndpointNotAllowed, PerTxLimit,
		VelocityLimit, BudgetExhausted, UnsupportedTerms, Killed}
	for _, e := range policy {
		want := e.Code() == "VELOCITY_LIMIT"
		if e.Retriable() != want {
			t.Errorf("%s retriable = %v, want %v", e.Code(), e.Retriable(), want)
		}
	}
}

// A resource belonging to somebody else must be indistinguishable from one that does not exist.
// A 403 there confirms the identifier is real, which is an enumeration oracle.
func TestNotFoundRatherThanForbiddenForAnotherOrganisation(t *testing.T) {
	if NotFound.Status() != 404 {
		t.Errorf("NotFound status = %d", NotFound.Status())
	}
}

func TestIsComparesByCodeThroughAWrappedChain(t *testing.T) {
	err := BudgetExhausted.Wrapping(os.ErrClosed)
	if !Is(err, "BUDGET_EXHAUSTED") {
		t.Error("Is failed to match the code")
	}
	if _, ok := As(err); !ok {
		t.Error("As failed to extract the fault")
	}
}
