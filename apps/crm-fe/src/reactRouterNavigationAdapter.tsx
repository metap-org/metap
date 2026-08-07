import type { ReactNode } from "react";
import { Link as RouterLink, useNavigate } from "react-router-dom";
import { NavigationContext } from "@metap/platform-react";
import type { NavigationAdapter } from "@metap/platform-react";

function useReactRouterNavigationAdapter(): NavigationAdapter {
  const navigate = useNavigate();

  return {
    toRecordList: (entityName) => `/records/${entityName}`,
    toNewRecord: (entityName) => `/records/${entityName}/new`,
    toRecordDetail: (entityName, id) => `/records/${entityName}/${id}`,
    toEditRecord: (entityName, id) => `/records/${entityName}/${id}/edit`,
    toLogin: () => "/dev-login",
    navigate,
    Link: RouterLink,
  };
}

export function ReactRouterNavigationProvider({ children }: { children: ReactNode }) {
  const adapter = useReactRouterNavigationAdapter();
  return <NavigationContext.Provider value={adapter}>{children}</NavigationContext.Provider>;
}
