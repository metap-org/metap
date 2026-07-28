export class AuthError extends Error {
  readonly statusCode = 401;
  readonly code = "unauthorized";

  constructor(message: string) {
    super(message);
    this.name = "AuthError";
  }
}
