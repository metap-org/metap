import { generateKeyPairSync } from "node:crypto";
import jwt from "jsonwebtoken";
import { describe, expect, it } from "vitest";
import { AuthError } from "./errors";
import { verifyToken } from "./jwt-verifier";

function makeKeyPair() {
  return generateKeyPairSync("rsa", {
    modulusLength: 2048,
    publicKeyEncoding: { type: "spki", format: "pem" },
    privateKeyEncoding: { type: "pkcs8", format: "pem" },
  });
}

describe("verifyToken", () => {
  it("returns claims for a validly signed token", () => {
    const { publicKey, privateKey } = makeKeyPair();
    const token = jwt.sign({ tenantId: "tenant-1", roles: ["admin"] }, privateKey, {
      algorithm: "RS256",
      subject: "user-1",
      expiresIn: "1h",
    });

    const claims = verifyToken(token, publicKey);
    expect(claims).toMatchObject({ sub: "user-1", tenantId: "tenant-1", roles: ["admin"] });
    expect(typeof claims.exp).toBe("number");
  });

  it("rejects a token signed with a different key", () => {
    const { privateKey } = makeKeyPair();
    const { publicKey: otherPublicKey } = makeKeyPair();
    const token = jwt.sign({ tenantId: "tenant-1" }, privateKey, {
      algorithm: "RS256",
      subject: "user-1",
    });

    expect(() => verifyToken(token, otherPublicKey)).toThrow(AuthError);
  });

  it("rejects a token missing the tenantId claim", () => {
    const { publicKey, privateKey } = makeKeyPair();
    const token = jwt.sign({}, privateKey, { algorithm: "RS256", subject: "user-1" });

    expect(() => verifyToken(token, publicKey)).toThrow(AuthError);
  });
});
