import { ZodError } from "zod";
import type { FastifyInstance, FastifyReply, FastifyRequest } from "fastify";
import { AuthError } from "../core/auth/errors";
import type { ServiceResult } from "../core/crud/result";

type ErrorBody = {
  error: {
    code: string;
    message: string;
    requestId: string;
    traceId: string;
  };
};

function errorBody(request: FastifyRequest, code: string, message: string): ErrorBody {
  return {
    error: {
      code,
      message,
      requestId: request.id,
      traceId: request.traceId,
    },
  };
}

export function registerErrorHandler(app: FastifyInstance) {
  app.setErrorHandler((error, request, reply) => {
    if (error instanceof AuthError) {
      request.log.warn({ err: error }, "auth rejected");
      return reply.code(error.statusCode).send(errorBody(request, error.code, error.message));
    }

    if (error instanceof ZodError) {
      return reply
        .code(400)
        .send(errorBody(request, "validation_failed", "Request validation failed."));
    }

    const httpError = error as { statusCode?: unknown; validation?: unknown; message?: unknown };

    if (
      typeof httpError.statusCode === "number" &&
      httpError.statusCode >= 400 &&
      httpError.statusCode < 500
    ) {
      const statusCode = httpError.statusCode;
      const code =
        statusCode === 429
          ? "too_many_requests"
          : httpError.validation
            ? "validation_failed"
            : "bad_request";
      const message = typeof httpError.message === "string" ? httpError.message : "Request failed.";

      request.log.warn({ err: error }, "client error");
      return reply.code(statusCode).send(errorBody(request, code, message));
    }

    request.log.error(error);
    return reply.code(500).send(errorBody(request, "internal_error", "Internal server error."));
  });

  app.setNotFoundHandler((request, reply) => {
    reply.code(404).send(errorBody(request, "not_found", "Route not found."));
  });
}

const SERVICE_ERROR_MESSAGES: Record<string, string> = {
  entity_not_found: "Entity not found.",
  forbidden: "You do not have permission to perform this action.",
  validation_failed: "Request validation failed.",
  insert_failed: "Failed to create the record.",
  record_not_found: "Record not found.",
  version_conflict: "The record was modified by someone else. Reload and try again.",
  no_workflow: "This entity has no workflow.",
  invalid_transition: "This transition is not valid from the record's current state.",
  guard_failed: "This transition is not allowed.",
};

export function sendServiceError(
  request: FastifyRequest,
  reply: FastifyReply,
  result: Extract<ServiceResult<unknown>, { ok: false }>,
) {
  const message = result.message ?? SERVICE_ERROR_MESSAGES[result.error] ?? result.error;
  return reply.code(result.status).send(errorBody(request, result.error, message));
}
