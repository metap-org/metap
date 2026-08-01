import { generateKeyPairSync } from "node:crypto";
import Fastify from "fastify";
import jwt from "jsonwebtoken";
import { describe, expect, it } from "vitest";
import { verifyToken } from "../../core/auth/jwt-verifier";
import type { JwtVerifier } from "../../core/auth/jwt-verifier";
import type { RoleAssignmentService } from "../../core/auth/role-assignment-service";
import { registerErrorHandler } from "../error-handler";
import { registerAuthHook } from "./auth-hook";
import { registerRequestContextHooks } from "./request-id";

function buildTestApp(
  verifier: JwtVerifier,
  roleAssignments: Pick<RoleAssignmentService, "getRolesForUser">,
) {
  const app = Fastify();

  registerRequestContextHooks(app);
  registerErrorHandler(app);
  registerAuthHook(app, verifier, roleAssignments);

  app.get("/protected", async (request) => ({ context: request.context }));

  return app;
}

describe("auth hook", () => {
  const { publicKey, privateKey } = generateKeyPairSync("rsa", {
    modulusLength: 2048,
    publicKeyEncoding: { type: "spki", format: "pem" },
    privateKeyEncoding: { type: "pkcs8", format: "pem" },
  });
  const verifier: JwtVerifier = {
    verify: (token) => verifyToken(token, publicKey),
  };

  it("rejects a request with no authorization header", async () => {
    const roleAssignments = { getRolesForUser: async () => [] };
    const app = buildTestApp(verifier, roleAssignments);
    const response = await app.inject({ method: "GET", url: "/protected" });

    expect(response.statusCode).toBe(401);
    expect(response.json()).toMatchObject({ error: { code: "unauthorized" } });
  });

  it("attaches request context with roles resolved from RoleAssignmentService", async () => {
    const roleAssignments = { getRolesForUser: async () => ["admin"] };
    const app = buildTestApp(verifier, roleAssignments);
    const token = jwt.sign({ tenantId: "tenant-1" }, privateKey, {
      algorithm: "RS256",
      subject: "user-1",
      expiresIn: "1h",
    });

    const response = await app.inject({
      method: "GET",
      url: "/protected",
      headers: { authorization: `Bearer ${token}` },
    });

    expect(response.statusCode).toBe(200);
    expect(response.json()).toEqual({
      context: { tenantId: "tenant-1", userId: "user-1", roles: ["admin"] },
    });
  });

  it("ignores a roles claim embedded in the JWT — roles always come from RoleAssignmentService", async () => {
    const roleAssignments = { getRolesForUser: async () => ["admin"] };
    const app = buildTestApp(verifier, roleAssignments);
    const token = jwt.sign({ tenantId: "tenant-1", roles: ["superadmin"] }, privateKey, {
      algorithm: "RS256",
      subject: "user-1",
      expiresIn: "1h",
    });

    const response = await app.inject({
      method: "GET",
      url: "/protected",
      headers: { authorization: `Bearer ${token}` },
    });

    expect(response.statusCode).toBe(200);
    expect(response.json()).toEqual({
      context: { tenantId: "tenant-1", userId: "user-1", roles: ["admin"] },
    });
  });
});
