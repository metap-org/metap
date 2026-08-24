import { useEffect } from "react";
import { Center, Text } from "@mantine/core";
import { useAuth } from "./AuthContext";
import { useNavigationAdapter } from "../navigation/NavigationContext";

/**
 * Route target for `crates/metap-auth::OidcConfig`'s `post_login_redirect` — the backend's
 * `GET /auth/oidc/{tenantId}/callback` appends the minted session JWT as a URL fragment
 * (`#token=...`, never a query param: fragments never reach server access logs or `Referer`
 * headers), so this page's only job is to read it out of `window.location.hash` and hand it to
 * `setToken`, exactly like `LoginForm` does after a local login — from here on an OIDC session is
 * indistinguishable from a local one to the rest of the app.
 */
export function OidcCallbackPage() {
  const { setToken } = useAuth();
  const navAdapter = useNavigationAdapter();

  useEffect(() => {
    const hash = window.location.hash;
    const token = hash.startsWith("#token=") ? decodeURIComponent(hash.slice("#token=".length)) : null;
    if (token) {
      setToken(token);
      navAdapter.navigate(navAdapter.toHome());
    }
  }, [setToken, navAdapter]);

  return (
    <Center mih="60vh">
      <Text>Signing you in…</Text>
    </Center>
  );
}
