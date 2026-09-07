# x402 — the wire contract, and where it comes from

These files are **copied, not written**. They come from the Solana Foundation's `pay-kit`
(`github.com/solana-foundation/pay-kit`, v0.7.0), which is the reference implementation of the
x402 `exact` scheme on Solana and the thing `pay.sh` and the `pay` CLI speak.

They are here because Leash must interoperate with agents and endpoints we do not control. An
agent using `pay curl` has to be able to pay an endpoint through our signer, and our signer has to
be able to sign a challenge from an endpoint that never heard of us. That only works if we speak
the protocol exactly, and the only way to be sure is to test against the same vectors every other
SDK is tested against.

| File | What it is |
|---|---|
| `exact-v1-spec.md` | The normative wire contract for x402 `exact` v1 on Solana |
| `x402-v1-build.json` | Vectors: building a v1 `X-PAYMENT` envelope from an offer |
| `x402-v1-verify.json` | Vectors: a server verifying a v1 credential |
| `x402-build.json` · `x402-verify.json` | The same for v2, which is pay-kit's default producer |

**pay-kit has no Go SDK.** Its SDKs are TypeScript, Rust, Python, Ruby, PHP, Lua, Kotlin and
Swift; the Go module path resolves only because Go's proxy will serve any tagged repository. Our
signer and sample endpoint are Go, so the wire format is implemented here — but implemented
against their spec and checked against their vectors, rather than invented.

**Updating these** means bumping the pay-kit version, re-copying, and re-running
`go test ./internal/domain/challenge/`. If a vector starts failing, the protocol moved and we
have not; that is the whole point of keeping them.
