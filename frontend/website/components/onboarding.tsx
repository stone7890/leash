"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { base64ToBytes, bytesToBase64 } from "@/lib/domain/base64";
import { COPY } from "@/lib/domain/fault";
import { TierBadge } from "@/components/pills";
import { HandshakeLog } from "@/components/handshake-log";
import type { Tier } from "@/lib/domain/state";

// The five-step onboarding, from the specification's flow P1.
//
// It ends in a REAL payment. Step 5 does not animate anything: it starts the actual loop against
// the sample endpoint and streams the handshake rows the signer and indexer wrote.

type Template = {
  id: string; name: string; description: string;
  cap: string; perTxMax: string; velocityMax: string;
  expiryDays: number; allowHosts: string[];
};

type Intent = {
  transaction: string; blockhash: string; expires_at: string; allowance_account: string;
  summary: {
    cap: string; expiry_ts: string; delegate_to: string;
    network_fee: string; rent_sol: string; funds_move: boolean; expiry_tier: Tier;
    mint: string; token_program: string;
  };
};

type SolanaProvider = {
  connect: () => Promise<{ publicKey: { toString(): string } }>;
  signTransaction: <T>(tx: T) => Promise<T>;
  signAndSendTransaction?: (tx: unknown) => Promise<{ signature: string }>;
};

export function Onboarding({ templates, demoHost }:
  { templates: Template[]; demoHost: string }) {
  // The sample endpoint step 5 pays. It goes on every new sandbox agent's allow list, visibly, as
  // a chip the owner can remove — because S2 checks the host of the test payment against that list,
  // and a wizard whose own last step is refused by the policy its second step wrote is a wizard
  // that cannot be completed. Adding it silently at payment time would be worse: the policy shown
  // would not be the policy stored.
  const withDemo = useCallback((list: string[]) =>
    list.includes(demoHost) ? list : [...list, demoHost], [demoHost]);

  const [step, setStep] = useState(1);
  const [template, setTemplate] = useState(templates[0]?.id ?? "blank");
  const [name, setName] = useState("research-bot-01");
  const [runsAs, setRunsAs] = useState("cli");
  const [cap, setCap] = useState(templates[0]?.cap ?? "10.000000");
  const [expiryDays, setExpiryDays] = useState(7);
  const [hosts, setHosts] = useState<string[]>(
    templates[0]?.allowHosts ? [...templates[0].allowHosts, demoHost] : [demoHost]);
  const [hostDraft, setHostDraft] = useState("");
  const [allowAll, setAllowAll] = useState(false);

  const [agent, setAgent] = useState<{ id: string; api_key: string; pubkey: string } | null>(null);
  const [intent, setIntent] = useState<Intent | null>(null);
  const [signState, setSignState] = useState<"idle" | "waiting" | "pending" | "rejected">("idle");
  const [error, setError] = useState<string | null>(null);
  const [paymentId, setPaymentId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // What the wallet HOLDS, read from the chain — not whether the faucet was clicked. `null` means
  // "not read", which includes a read that failed, and null does not block the button: being
  // unable to look is not evidence that the wallet is empty, and treating it as such is how this
  // screen used to lock people out.
  const [balance, setBalance] = useState<{ usdc: string; sol: string } | null>(null);
  const [reading, setReading] = useState(false);

  // Generated when the form OPENS, not when the button is clicked, so two clicks create one agent.
  const idempotencyKey = useMemo(() => crypto.randomUUID(), []);
  const intentKey = useMemo(() => crypto.randomUUID(), []);
  const testKey = useMemo(() => crypto.randomUUID(), []);
  const faucetKey = useMemo(() => crypto.randomUUID(), []);

  // A brand-new wallet holds no sandbox USDC and no SOL, and the delegation spends both. Asking
  // for them here — before the wallet prompt — means the owner never meets a transaction that
  // fails for a reason they cannot see.
  async function fund() {
    setBusy(true); setError(null);
    try {
      const res = await fetch("/v1/sandbox/faucet", {
        method: "POST",
        headers: { "content-type": "application/json", "idempotency-key": faucetKey },
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body?.error?.message ?? "the faucet could not fund this wallet");
      // The faucet waits for its own transfer to confirm before answering, so the chain already
      // agrees. Read it back rather than assuming what it now says.
      await readBalance();
    } catch (e) {
      setError(e instanceof Error ? e.message : "the faucet could not fund this wallet");
    } finally { setBusy(false); }
  }

  const readBalance = useCallback(async () => {
    setReading(true);
    try {
      const res = await fetch("/v1/sandbox/wallet", { cache: "no-store" });
      setBalance(res.ok ? await res.json() : null);
    } catch {
      setBalance(null);
    } finally { setReading(false); }
  }, []);

  // Read on arrival at the delegation step, because that is the step whose transaction spends it.
  useEffect(() => {
    if (step === 3) void readBalance();
  }, [step, readBalance]);

  // Step 4 says it is waiting for on-chain confirmation, so it has to actually wait for it. The
  // allowance is pending until the indexer reads it back (I4), and a test payment started inside
  // that window is refused by S1 — the signer being right, and the screen being wrong. Polling the
  // agent is what makes the sentence above the button true.
  const [allowanceState, setAllowanceState] = useState<string | null>(null);
  const [waitedOut, setWaitedOut] = useState(false);

  useEffect(() => {
    if (step !== 4 || !agent) return;
    let stopped = false;
    const read = async () => {
      try {
        const res = await fetch(`/v1/agents/${agent.id}`, { cache: "no-store" });
        if (!res.ok) return;
        const body = await res.json();
        if (!stopped) setAllowanceState(body?.allowance?.state ?? null);
      } catch {
        // Late, not wrong. The next tick asks again.
      }
    };
    void read();
    const poll = setInterval(read, 3000);
    // An escape hatch, because a screen that can only ever unlock itself is the failure this
    // wizard has already met once: if the read keeps failing, hand the decision to the signer,
    // which is the authority anyway and refuses with a message naming the rule.
    const escape = setTimeout(() => !stopped && setWaitedOut(true), 90_000);
    return () => { stopped = true; clearInterval(poll); clearTimeout(escape); };
  }, [step, agent]);

  const confirmed = allowanceState === "active";

  // Short means the chain SAYS it is short. An unread balance is not short: the button stays
  // available, and a wallet that really cannot afford the delegation learns it from the chain
  // rather than from a screen that guessed.
  const short = balance !== null && Number(balance.usdc) < Number(cap);

  const pickTemplate = useCallback((t: Template) => {
    setTemplate(t.id);
    setCap(t.cap);
    setHosts(withDemo(t.allowHosts));
    setExpiryDays(t.expiryDays);
  }, [withDemo]);

  async function createAgent() {
    setBusy(true); setError(null);
    try {
      const res = await fetch("/v1/agents", {
        method: "POST",
        headers: { "content-type": "application/json", "idempotency-key": idempotencyKey },
        body: JSON.stringify({
          name, template_id: template, network: "sandbox", runs_as: runsAs,
          cap, allow_hosts: hosts, allow_all: allowAll, expiry_days: expiryDays,
        }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body?.error?.message ?? "could not create the agent");
      setAgent({ id: body.id, api_key: body.api_key, pubkey: body.pubkey });
      setStep(3);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not create the agent");
    } finally { setBusy(false); }
  }

  async function buildAndSign() {
    if (!agent) return;
    setBusy(true); setError(null); setSignState("waiting");
    try {
      // Built immediately before the wallet opens. A Solana blockhash lasts 60–90 seconds and a
      // person can be interrupted, so the transaction is made as late as possible.
      const res = await fetch(`/v1/agents/${agent.id}/delegation-intents`, {
        method: "POST",
        headers: { "content-type": "application/json", "idempotency-key": intentKey },
        body: JSON.stringify({ cap, expiry_days: expiryDays }),
      });
      const built = await res.json();
      if (!res.ok) throw new Error(built?.error?.message ?? "could not build the transaction");
      setIntent(built);

      const provider = (window as never as { phantom?: { solana?: SolanaProvider } }).phantom?.solana
        ?? (window as never as { solflare?: SolanaProvider }).solflare;
      if (!provider) throw new Error("no wallet was detected in this browser");
      await provider.connect();

      const { Transaction } = await import("@solana/web3.js");
      const tx = Transaction.from(base64ToBytes(built.transaction));
      const signed = await provider.signTransaction(tx);
      const encoded = bytesToBase64(
        (signed as unknown as { serialize(): Uint8Array }).serialize(),
      );

      const sent = await fetch(`/v1/agents/${agent.id}/submit`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          transaction: encoded,
          allowance_account: built.allowance_account,
          mint: built.summary.mint,
          token_program: built.summary.token_program,
          cap, expiry_days: expiryDays, expiry_tier: built.summary.expiry_tier,
        }),
      });
      const out = await sent.json();
      if (!sent.ok) throw new Error(out?.error?.message ?? "the chain refused the transaction");

      // Submitted is NOT confirmed. It stays pending until the indexer reads it back.
      setSignState("pending");
      setStep(4);
    } catch (e) {
      // The wallet-rejection branch is built, not just the happy one. An interface that only
      // handles success teaches its user that a refusal is a bug.
      setSignState("rejected");
      setError(e instanceof Error ? e.message : COPY.walletRejected);
    } finally { setBusy(false); }
  }

  async function runTestPayment() {
    if (!agent) return;
    setBusy(true); setError(null);
    try {
      const res = await fetch("/v1/test-payments", {
        method: "POST",
        headers: { "content-type": "application/json", "idempotency-key": testKey },
        body: JSON.stringify({ agent_id: agent.id, api_key: agent.api_key }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body?.error?.message ?? "the test payment could not start");
      setPaymentId(body.payment_id);
      setStep(5);
    } catch (e) {
      setError(e instanceof Error ? e.message : "the test payment could not start");
    } finally { setBusy(false); }
  }

  return (
    <div className="mx-auto max-w-2xl">
      <Steps current={step} />

      {error && (
        <p className="mb-4 rounded-[10px] border border-red/30 bg-redd px-3 py-2 text-sm text-red">
          {error}
        </p>
      )}

      {step === 1 && (
        <section className="card space-y-4">
          <h2 className="text-lg font-medium">1 · Start from a template</h2>
          <div className="grid gap-2 sm:grid-cols-3">
            {templates.map((t) => (
              <button key={t.id} onClick={() => pickTemplate(t)}
                className={`rounded-[10px] border p-3 text-left text-sm ${
                  template === t.id ? "border-grn bg-panel2" : "border-line bg-panel2"}`}>
                <div className="font-medium">{t.name}</div>
                <div className="mt-1 text-xs text-mut">{t.description}</div>
              </button>
            ))}
          </div>
          <Field label="Agent name">
            <input value={name} onChange={(e) => setName(e.target.value)} className={inputCls} />
          </Field>
          <Field label="Runs as">
            <select value={runsAs} onChange={(e) => setRunsAs(e.target.value)} className={inputCls}>
              <option value="mcp">MCP server (Claude, etc.)</option>
              <option value="cli">CLI / script (pay, curl)</option>
              <option value="hosted">Hosted service</option>
            </select>
          </Field>
          <p className="text-xs text-mut">
            Leash generates a dedicated keypair for this agent inside the signer. The key never
            leaves it — your agent gets an API key instead.
          </p>
          <button onClick={() => setStep(2)} className={primaryCls}>Continue</button>
        </section>
      )}

      {step === 2 && (
        <section className="card space-y-4">
          <h2 className="text-lg font-medium">2 · Budget and rules</h2>

          <Field label="Spending cap" badge={<TierBadge tier="onchain" />}>
            {/* type="text", never type="number". A cap that has been through a JavaScript number
                is not a cap (I6). */}
            <input type="text" inputMode="decimal" value={cap}
                   onChange={(e) => setCap(e.target.value)} className={inputCls} />
            <div className="mt-1 flex gap-1">
              {["5.000000", "10.000000", "50.000000"].map((v) => (
                <button key={v} onClick={() => setCap(v)}
                        className="rounded-[10px] border border-line px-2 py-0.5 text-xs text-mut hover:text-ink">
                  ${v.replace(/(\.\d\d)\d*$/, "$1")}
                </button>
              ))}
            </div>
          </Field>

          <Field label="Expires" badge={<TierBadge tier="signer" />}>
            <select value={expiryDays} onChange={(e) => setExpiryDays(Number(e.target.value))}
                    className={inputCls}>
              <option value={1}>24 hours</option>
              <option value={7}>7 days</option>
              <option value={30}>30 days</option>
            </select>
            {/* Honest tiering: the shipping adapter has no on-chain expiry, so this is a signer
                rule and the badge says so. I3 does not permit claiming otherwise. */}
            <p className="mt-1 text-xs text-mut">
              Expiry is checked by the Leash signer. The cap is enforced by Solana itself.
            </p>
          </Field>

          <Field label="Allowed endpoints" badge={<TierBadge tier="signer" />}>
            <div className="mb-2 flex flex-wrap gap-1.5">
              {hosts.map((h) => (
                <span key={h} className="mono rounded-full border border-line bg-panel2 px-2 py-0.5 text-xs">
                  {h}
                  {h === demoHost && <span className="ml-1 text-mut">· sample endpoint</span>}
                  <button onClick={() => setHosts(hosts.filter((x) => x !== h))}
                          className="ml-1.5 text-mut hover:text-red">×</button>
                </span>
              ))}
              {allowAll && (
                <span className="rounded-full border border-amb/40 bg-panel2 px-2 py-0.5 text-xs text-amb">
                  all endpoints allowed
                </span>
              )}
            </div>
            <input value={hostDraft} placeholder="add a host, press Enter"
              onChange={(e) => setHostDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key !== "Enter") return;
                e.preventDefault();
                const v = hostDraft.trim().toLowerCase();
                if (!v) return;
                if (v === "*") { setAllowAll(true); setHostDraft(""); return; }
                if (!hosts.includes(v)) setHosts([...hosts, v]);
                setHostDraft("");
              }}
              className={inputCls} />
            <p className="mt-1 text-xs text-mut">
              Type <span className="mono">*</span> to allow every endpoint — we&apos;ll flag the
              agent with a warning if you do. <span className="mono">{demoHost}</span> is the
              sandbox&apos;s sample endpoint, which step 5 pays for real; remove it and the test
              payment will be refused by rule S2, which is the rule working.
            </p>
          </Field>

          <div className="flex gap-2">
            <button onClick={() => setStep(1)} className={secondaryCls}>Back</button>
            <button onClick={createAgent} disabled={busy} className={primaryCls}>
              {busy ? "Creating…" : "Create agent"}
            </button>
          </div>
        </section>
      )}

      {step === 3 && agent && (
        <section className="card space-y-4">
          <h2 className="text-lg font-medium">3 · Sign the delegation</h2>
          <p className="text-sm text-mut">
            One transaction from your wallet creates the on-chain allowance.
          </p>

          <dl className="mono space-y-1 rounded-[10px] border border-line bg-panel2 p-3 text-xs">
            <Row k="Agent" v={name} />
            <Row k="Network" v="Sandbox" />
            <Row k="Cap" v={`$${cap} USDC`} />
            <Row k="Expires" v={`in ${expiryDays} days`} />
            <Row k="Delegate to" v={`${agent.pubkey.slice(0, 6)}…${agent.pubkey.slice(-4)}`} />
            {intent && <Row k="Network fee + rent" v={`~${intent.summary.rent_sol} SOL`} />}
          </dl>

          {/* Under the SPL delegate the owner's USDC MOVES into a per-agent account they still
              own. The prototype's "funds stay put" would be false, so it is not used. */}
          <p className="rounded-[10px] border border-amb/30 bg-panel2 p-3 text-xs text-mut">
            Your ${cap} USDC moves into a token account for this agent that{" "}
            <span className="text-ink">you still own and can empty at any time</span>. The agent
            may spend up to the cap from it, and not one unit more — Solana enforces that with or
            without Leash running.
          </p>

          {short && (
            <div className="rounded-[10px] border border-line bg-panel2 p-3">
              <p className="text-xs text-mut">
                {Number(balance?.usdc ?? 0) === 0
                  ? "This wallet needs sandbox USDC before it can delegate any. It is play money on a test network and has no value."
                  : `This wallet holds $${balance?.usdc} sandbox USDC, and the delegation moves $${cap} into the agent's account.`}
              </p>
              <button onClick={fund} disabled={busy}
                      className="mt-2 rounded-[10px] border border-line px-3 py-1.5 text-xs
                                 text-ink hover:border-grn disabled:opacity-50">
                {busy ? "Sending…" : "Send me 100 test USDC"}
              </button>
            </div>
          )}
          {balance && !short && (
            <p className="mono text-xs text-grn">
              Wallet holds ${balance.usdc} test USDC · {balance.sol} SOL
            </p>
          )}

          {signState === "waiting" && (
            <p className="text-xs text-amb">Waiting for your wallet…</p>
          )}
          {signState === "rejected" && (
            <p className="text-xs text-red">{COPY.walletRejected}</p>
          )}

          <div className="flex gap-2">
            <button onClick={buildAndSign} disabled={busy || reading || short} className={primaryCls}>
              {signState === "rejected" ? "Try again"
                : busy ? "Building…"
                : reading ? "Checking your wallet…"
                : "Sign in wallet"}
            </button>
          </div>
        </section>
      )}

      {step === 4 && agent && (
        <section className="card space-y-4">
          <h2 className="text-lg font-medium">4 · Point your agent at Leash</h2>

          {/* Submitted is not confirmed. The allowance is pending until the indexer reads it
              back, and saying otherwise would be reporting success early (I4). */}
          {confirmed ? (
            <p className="rounded-[10px] border border-grn/30 bg-panel2 p-3 text-xs text-grn">
              Confirmed on Solana. The allowance is active and this agent can spend from it.
            </p>
          ) : (
            <p className="rounded-[10px] border border-amb/30 bg-panel2 p-3 text-xs text-mut">
              Waiting for on-chain confirmation. Submitted is not confirmed — this flips the moment
              the allowance is readable on Solana.
            </p>
          )}

          <Field label="Agent API key">
            <div className="mono break-all rounded-[10px] border border-line bg-panel2 p-3 text-xs">
              {agent.api_key}
            </div>
            <p className="mt-1 text-xs text-mut">
              Copy it now — it will not be shown again.
            </p>
          </Field>

          <Field label="CLI">
            <pre className="mono overflow-x-auto rounded-[10px] border border-line bg-panel2 p-3 text-xs">
{`export LEASH_SIGNER=${typeof window === "undefined" ? "" : window.location.origin}
export LEASH_AGENT_KEY=${agent.api_key}
pay curl -i https://api.exa.ai/search?q=solana   # 402 → sign → 200`}
            </pre>
          </Field>

          <div className="flex gap-2">
            <button onClick={runTestPayment} disabled={busy || !(confirmed || waitedOut)}
                    className={primaryCls}>
              {busy ? "Starting…"
                : confirmed || waitedOut ? "Run a test payment"
                : "Waiting for confirmation…"}
            </button>
            <a href="/agents" className={secondaryCls}>Skip — open dashboard</a>
          </div>
        </section>
      )}

      {step === 5 && paymentId && (
        <section className="card space-y-4">
          <h2 className="text-lg font-medium">5 · Watch the first payment</h2>
          <p className="text-sm text-mut">
            We&apos;re calling a sample paid endpoint on the sandbox as your agent. This is the
            exact 402 handshake it will run in production.
          </p>
          {/* Real server-sent events, one line per handshake row. Not an animation. */}
          <HandshakeLog paymentId={paymentId} />
          <a href="/agents" className={primaryCls}>Open dashboard</a>
        </section>
      )}
    </div>
  );
}

const inputCls =
  "w-full rounded-[10px] border border-line bg-panel2 px-3 py-2 text-sm outline-none focus:border-pur";
const primaryCls =
  "inline-block rounded-[10px] bg-grn px-4 py-2 text-sm font-medium text-bg disabled:opacity-50";
const secondaryCls =
  "inline-block rounded-[10px] border border-line px-4 py-2 text-sm text-mut hover:text-ink";

function Steps({ current }: { current: number }) {
  return (
    <div className="mb-6 flex gap-1.5">
      {[1, 2, 3, 4, 5].map((n) => (
        <div key={n}
             className={`h-1 flex-1 rounded-full ${n <= current ? "bg-grn" : "bg-line"}`} />
      ))}
    </div>
  );
}

function Field({ label, badge, children }: {
  label: string; badge?: React.ReactNode; children: React.ReactNode;
}) {
  return (
    <div>
      <div className="mb-1.5 flex items-center gap-2">
        <label className="text-xs font-medium text-mut">{label}</label>
        {badge}
      </div>
      {children}
    </div>
  );
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex justify-between gap-4">
      <dt className="text-mut">{k}</dt>
      <dd className="text-ink">{v}</dd>
    </div>
  );
}
