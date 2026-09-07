// Base64 for the browser.
//
// `Buffer` is a Node global. Next.js does not polyfill it into the client bundle, so a component
// that reaches for it throws `Buffer is not defined` the moment a real person opens the page —
// and only then, because every server-rendered path has it. This is the shape of bug that passes
// every test and fails the demo.

export function base64ToBytes(b64: string): Uint8Array {
  const binary = atob(b64);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}

export function bytesToBase64(bytes: Uint8Array): string {
  // In chunks: String.fromCharCode(...bytes) blows the argument limit on a transaction of any
  // size, and a delegation is several hundred bytes.
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
}
