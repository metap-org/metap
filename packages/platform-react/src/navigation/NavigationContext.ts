import { createContext, useContext } from "react";
import type { Context, FunctionComponent, ReactNode } from "react";

export type NavigationAdapter = {
  toRecordList: (entityName: string) => string;
  toNewRecord: (entityName: string) => string;
  toRecordDetail: (entityName: string, id: string) => string;
  toEditRecord: (entityName: string, id: string) => string;
  toLogin: () => string;
  navigate: (path: string) => void;
  Link: FunctionComponent<{ to: string; children: ReactNode }>;
};

export const NavigationContext: Context<NavigationAdapter | null> =
  createContext<NavigationAdapter | null>(null);

export function useNavigationAdapter(): NavigationAdapter {
  const adapter = useContext(NavigationContext);
  if (!adapter) {
    throw new Error(
      "useNavigationAdapter() called with no NavigationContext.Provider above it — every packages/platform-react consumer must provide one.",
    );
  }
  return adapter;
}
