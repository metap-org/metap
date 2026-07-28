import fs from "node:fs";
import jwt from "jsonwebtoken";
import { z } from "zod";
import { AuthError } from "./errors";

const ClaimsSchema = z.object({
  sub: z.string().min(1),
  tenantId: z.string().min(1),
  roles: z.array(z.string()).default([]),
  functionId: z.string().optional(),
  exp: z.number(),
});

export type Claims = z.infer<typeof ClaimsSchema>;

export function verifyToken(token: string, publicKey: string): Claims {
  let payload: unknown;

  try {
    payload = jwt.verify(token, publicKey, { algorithms: ["RS256"] });
  } catch {
    throw new AuthError("Invalid or expired token.");
  }

  const parsed = ClaimsSchema.safeParse(payload);

  if (!parsed.success) {
    throw new AuthError("Token is missing required claims.");
  }

  return parsed.data;
}

export type JwtVerifier = {
  verify(token: string): Claims;
};

export function createJwtVerifier(publicKeyPath: string): JwtVerifier {
  const publicKey = fs.readFileSync(publicKeyPath, "utf8");

  return {
    verify(token: string) {
      return verifyToken(token, publicKey);
    },
  };
}
