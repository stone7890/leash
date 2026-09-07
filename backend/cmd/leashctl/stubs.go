package main

import (
	"context"
	"errors"
)

// Not yet written. Each returns an error rather than succeeding silently, because a verification
// command that prints nothing and exits zero is worse than one that does not exist: CI would go
// green on a proof that never ran.

func cmdVerifyPrivileges(context.Context, []string) error {
	return errors.New("verify-privileges is not implemented yet")
}

func cmdVerifyContracts(context.Context, []string) error {
	return errors.New("verify-contracts is not implemented yet")
}

func cmdVerifyArchitecture(context.Context, []string) error {
	return errors.New("verify-architecture is not implemented yet")
}

func cmdSeed(context.Context, []string) error {
	return errors.New("seed is not implemented yet")
}
