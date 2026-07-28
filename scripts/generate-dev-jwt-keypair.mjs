import { generateKeyPairSync } from "node:crypto";
import { mkdirSync, writeFileSync } from "node:fs";

const { publicKey, privateKey } = generateKeyPairSync("rsa", {
  modulusLength: 2048,
  publicKeyEncoding: { type: "spki", format: "pem" },
  privateKeyEncoding: { type: "pkcs8", format: "pem" },
});

mkdirSync("keys", { recursive: true });
writeFileSync("keys/dev-jwt-public.pem", publicKey);
writeFileSync("keys/dev-jwt-private.pem", privateKey);

console.log("Generated dev JWT keypair in ./keys (gitignored).");
console.log("Set in .env: AUTH_JWT_PUBLIC_KEY_PATH=./keys/dev-jwt-public.pem");
