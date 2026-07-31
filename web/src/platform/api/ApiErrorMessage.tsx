import { Link } from "react-router-dom";
import { ApiError } from "./client";

export function ApiErrorMessage({ error }: { error: unknown }) {
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div>
        Session expired. <Link to="/dev-login">Sign in again</Link>.
      </div>
    );
  }

  return <div>Error: {error instanceof Error ? error.message : String(error)}</div>;
}
