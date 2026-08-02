import jwt from "jsonwebtoken";
import { readFileSync } from "node:fs";

const tenantId = process.argv[2] ?? "00000000-0000-0000-0000-000000000001";
const userId = process.argv[3] ?? "00000000-0000-0000-0000-000000000002";

const privateKey = readFileSync("keys/dev-jwt-private.pem", "utf8");

const token = jwt.sign({ tenantId }, privateKey, {
  algorithm: "RS256",
  subject: userId,
  expiresIn: "1h",
});

console.log(token);
