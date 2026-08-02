import { randomUUID } from "node:crypto";
import type { FastifyInstance } from "fastify";

declare module "fastify" {
  interface FastifyRequest {
    traceId: string;
  }
}

const TRACE_ID_PATTERN = /^[a-zA-Z0-9-]{1,128}$/;

export function registerRequestContextHooks(app: FastifyInstance) {
  app.addHook("onRequest", (request, reply, done) => {
    const incomingTraceId = request.headers["x-trace-id"];
    const traceId =
      typeof incomingTraceId === "string" && TRACE_ID_PATTERN.test(incomingTraceId)
        ? incomingTraceId
        : randomUUID();

    request.traceId = traceId;
    request.log = request.log.child({ requestId: request.id, traceId });

    reply.header("x-request-id", request.id);
    reply.header("x-trace-id", traceId);

    done();
  });
}
