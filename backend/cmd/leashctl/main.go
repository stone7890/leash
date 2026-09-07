// leashctl is the tool that proves what the prose claims.
//
// The prose in docs/ says the schema refuses certain writes, that migrations reverse, that the
// append-only collections are append-only, and that the architecture's boundaries hold. Each of
// those is a claim, and each subcommand here turns one into a build failure.
//
// Every proof command ships with --selftest, and the self-test runs FIRST in CI. A gate nobody has
// watched reject something is a comment: the sibling project's equivalent once passed everything
// because a regex error made grep exit non-zero, which reads identically to "no match".
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const usage = `leashctl — prove what the documentation claims

  migrate [--to N]        apply migrations, or roll back to version N
  verify-migrations       up · snapshot · down · assert empty · up · assert identical
  verify-constraints      the writes the schema must refuse    [--selftest]
  verify-privileges       append-only, proved as the app role  [--selftest]
  verify-contracts        Go and the contracts/ files agree
  verify-architecture     the dependency and type boundaries   [--selftest]
  bootstrap               prepare a fresh sandbox: mint, faucet, recipient account
  keys                    the sandbox's addresses, their balances, and what is short
  faucet [address]        ask the chain for SOL; --sol N, --usdc N
  seed                    a demo organisation and agent, on the sandbox

Environment:
  MONGO_URI   must name a replica set — there is no standalone mode
  MONGO_DB    defaults to "leash"
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "migrate":
		err = cmdMigrate(ctx, args)
	case "verify-migrations":
		err = cmdVerifyMigrations(ctx, args)
	case "verify-constraints":
		err = cmdVerifyConstraints(ctx, args)
	case "verify-privileges":
		err = cmdVerifyPrivileges(ctx, args)
	case "verify-contracts":
		err = cmdVerifyContracts(ctx, args)
	case "verify-architecture":
		err = cmdVerifyArchitecture(ctx, args)
	case "bootstrap":
		err = cmdBootstrap(ctx, args)
	case "keys":
		err = cmdKeys(ctx, args)
	case "faucet":
		err = cmdFaucet(ctx, args)
	case "demo":
		err = cmdDemo(ctx, args)
	case "seed":
		err = cmdSeed(ctx, args)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "leashctl: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}

	if err != nil {
		// Printed as well as returned: a container log reader may not have a level configured, and
		// a failure nobody can read is a failure nobody can fix.
		fmt.Fprintf(os.Stderr, "\nleashctl %s failed:\n  %v\n", cmd, err)
		os.Exit(1)
	}
}

func env(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func mongoURI() string {
	return env("MONGO_URI", "mongodb://localhost:27017/leash?replicaSet=rs0&directConnection=true")
}

func mongoDB() string { return env("MONGO_DB", "leash") }

func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}

func has(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"="), true
		}
	}
	return "", false
}
