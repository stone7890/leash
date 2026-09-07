import { describe, expect, it } from "vitest";
import { base64ToBytes, bytesToBase64 } from "./base64";

describe("base64 without Buffer", () => {
  it("round-trips a transaction-sized payload", () => {
    // A delegation is several hundred bytes, which is where a naive
    // String.fromCharCode(...bytes) hits the argument limit and throws.
    const bytes = new Uint8Array(1024);
    for (let i = 0; i < bytes.length; i++) bytes[i] = i % 256;
    expect(base64ToBytes(bytesToBase64(bytes))).toEqual(bytes);
  });

  it("handles every byte value, including the ones that are not valid text", () => {
    const bytes = new Uint8Array([0, 1, 127, 128, 200, 254, 255]);
    expect(bytesToBase64(bytes)).toBe("AAF/gMj+/w==");
    expect(base64ToBytes("AAF/gMj+/w==")).toEqual(bytes);
  });

  it("does not depend on Buffer", () => {
    // The whole point. If this module ever reaches for Buffer again, the browser breaks and
    // nothing on the server notices.
    expect(bytesToBase64.toString()).not.toContain("Buffer");
    expect(base64ToBytes.toString()).not.toContain("Buffer");
  });
});
