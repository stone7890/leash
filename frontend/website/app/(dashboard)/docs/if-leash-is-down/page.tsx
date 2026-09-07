import { readFileSync } from "node:fs";

export const dynamic = "force-dynamic";

/**
 * The self-serve revoke guide, served from the repository's own copy.
 *
 * It is rendered from docs/if-leash-is-down.md rather than retyped, because a second copy is a
 * copy that goes stale — and this is the one page whose incorrectness costs a customer money. An
 * owner following it during our outage who revokes but does not sweep leaves their balance in an
 * account they will not think to look at.
 */
export default function RevokeGuide() {
  let markdown: string;
  try {
    markdown = readFileSync("/docs/if-leash-is-down.md", "utf8");
  } catch {
    markdown = FALLBACK;
  }
  return (
    <article className="max-w-3xl">
      <pre className="whitespace-pre-wrap break-words text-sm leading-relaxed text-mut">
        {markdown}
      </pre>
    </article>
  );
}

// If the file is not mounted, the essential instructions still appear. A guide that 500s during an
// outage is a guide that does not exist.
const FALLBACK = `If Leash is ever down

Your control doesn't depend on us. Revoking is a transaction YOU sign, straight to Solana.

Two instructions, not one:

  1. Revoke the delegation — this stops the agent immediately and permanently.

       spl-token revoke <AGENT_TOKEN_ACCOUNT> --owner <YOUR_WALLET>

  2. Sweep the remaining balance back — do not skip this. Revoking alone leaves your money
     in an account you are unlikely to look at again.

       spl-token transfer <USDC_MINT> <AMOUNT> <YOUR_MAIN_ACCOUNT> \\
         --from <AGENT_TOKEN_ACCOUNT> --owner <YOUR_WALLET>

The agent's token account is shown on its page in Leash, and in your audit export.

Which network? The identifier tells you: agt_test_… is the sandbox (play money),
agt_live_… is mainnet (real USDC).

We will never ask for your seed phrase, your private key, or a signed blank transaction.
Anyone who does is not us.`;
