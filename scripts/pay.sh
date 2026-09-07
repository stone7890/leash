#!/usr/bin/env bash
#
# The x402 loop, by hand: 402 → sign → replay.
#
# The onboarding screen hands out a snippet that starts `pay curl …`. There is no `pay` in this
# repository — it is a client the specification imagines and this codebase has not written. What
# the system really offers an agent is ONE endpoint, `POST /v1/sign`, so this is that snippet's
# meaning spelled out in curl: useful for watching the handshake, and for checking a key by hand
# without the dashboard.
#
#   LEASH_AGENT_KEY=lk_test_… scripts/pay.sh
#   LEASH_AGENT_KEY=lk_test_… scripts/pay.sh http://localhost/demo/search?q=solana demo402:4200
#
# The second argument is the host the SIGNER checks against the agent's allow list (rule S2), and
# it is deliberately separate from the URL: the sample endpoint is reached through nginx from
# outside and by its container name from inside, and the allow list names the second one. Sending a
# host the agent may not pay is refused — which is the rule working, not a bug in this script.
#
# It needs python3 for the JSON, and nothing else.
set -euo pipefail

SIGNER="${LEASH_SIGNER:-http://localhost}"
KEY="${LEASH_AGENT_KEY:?set LEASH_AGENT_KEY to an agent key — the wizard shows it once, at step 4}"
URL="${1:-$SIGNER/demo/search?q=solana}"
HOST="${2:-demo402:4200}"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# ── 1 · the 402 ──────────────────────────────────────────────────────────────
# The body of a 402 IS the challenge. Nothing is parsed out of it here: it is forwarded to the
# signer verbatim, because the signer hashes it and that hash is what makes a challenge unrepeatable.
code=$(curl -sS -o "$work/challenge.json" -w '%{http_code}' "$URL")
if [ "$code" != "402" ]; then
  echo "expected 402 from $URL, got $code:" >&2
  cat "$work/challenge.json" >&2; echo >&2
  exit 1
fi
echo "402  · challenge received from $URL"

# ── 2 · the signature ────────────────────────────────────────────────────────
python3 - "$work/challenge.json" "$HOST" > "$work/request.json" <<'PY'
import json, sys
challenge = json.load(open(sys.argv[1]))
json.dump({"challenge": challenge, "host": sys.argv[2]}, sys.stdout)
PY

idem=$(python3 -c 'import uuid; print(uuid.uuid4())')
code=$(curl -sS -o "$work/signed.json" -w '%{http_code}' -X POST "$SIGNER/v1/sign" \
  -H 'content-type: application/json' \
  -H "X-Leash-Key: $KEY" \
  -H "Idempotency-Key: $idem" \
  --data-binary @"$work/request.json")

if [ "$code" != "200" ]; then
  # A 403 is a VERDICT, not a failure of this script: the signer refused, and it says which of the
  # eight rules did it. Printed as a line rather than a JSON dump, because the rule is the answer.
  python3 - "$work/signed.json" "$code" <<'PY' >&2
import json, sys
body = json.load(open(sys.argv[1]))
err = body.get("error", {})
d = err.get("details", {})
print(f"{sys.argv[2]}  · {err.get('code','refused')} — {err.get('message','')}")
for c in d.get("checks", []):
    if c.get("result") == "fail":
        print(f"      rule {c.get('rule')} ({c.get('name')}, {c.get('tier')} tier): {c.get('detail','')}")
PY
  exit 1
fi

header=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("header") or "X-PAYMENT")' "$work/signed.json")
payload=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["payload"])' "$work/signed.json")
python3 - "$work/signed.json" <<'PY'
import json, sys
b = json.load(open(sys.argv[1]))
print(f"200  · signed · payment {b['payment_id']}")
PY

# ── 3 · the replay ───────────────────────────────────────────────────────────
# The same request again, carrying the credential in the header the SIGNER named. Guessing the
# header would put a v2 credential in the v1 field for a v1 server, which sees nothing at all.
echo "     · replaying with $header"
curl -sS -i -H "$header: $payload" "$URL"
