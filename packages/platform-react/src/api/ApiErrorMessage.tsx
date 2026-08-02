import { useNavigationAdapter } from "../navigation/NavigationContext";
import { ApiError } from "./client";

export function ApiErrorMessage({ error }: { error: unknown }) {
  const adapter = useNavigationAdapter();

  if (error instanceof ApiError && error.status === 401) {
    return (
      <div>
        Session expired. <adapter.Link to={adapter.toLogin()}>Sign in again</adapter.Link>.
      </div>
    );
  }

  return <div>Error: {error instanceof Error ? error.message : String(error)}</div>;
}
