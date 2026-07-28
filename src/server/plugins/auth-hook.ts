import type { FastifyInstance } from "fastify";
import type { JwtVerifier } from "../../core/auth/jwt-verifier";
import { AuthError } from "../../core/auth/errors";
import type { RequestContext } from "../../core/permission/permission-service";
import { buildRequestContext } from "../../core/auth/request-context";

declare module "fastify" {
  interface FastifyRequest {
    context: RequestContext;
  }
}

const BEARER_PREFIX = "Bearer ";

export function registerAuthHook(instance: FastifyInstance, verifier: JwtVerifier) {
  instance.decorateRequest("context", null, []);

  instance.addHook("onRequest", (request, _reply, done) => {
    const header = request.headers.authorization;

    if (!header || !header.startsWith(BEARER_PREFIX)) {
      done(new AuthError("Missing or invalid authorization header."));
      return;
    }

    const token = header.slice(BEARER_PREFIX.length);

    try {
      const claims = verifier.verify(token);
      request.context = buildRequestContext(claims);
      done();
    } catch (error) {
      if (error instanceof AuthError) {
        done(error);
        return;
      }
      done(error instanceof Error ? error : new Error("Token verification failed."));
    }
  });
}
