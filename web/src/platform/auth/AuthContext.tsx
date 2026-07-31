import { createContext, useContext, useMemo, useState, useCallback } from "react";
import type { ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";

type AuthContextValue = {
  token: string | null;
  setToken: (token: string | null) => void;
};

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setTokenState] = useState<string | null>(null);
  const queryClient = useQueryClient();

  const setToken = useCallback(
    (newToken: string | null) => {
      queryClient.clear();
      setTokenState(newToken);
    },
    [queryClient],
  );

  const value = useMemo(() => ({ token, setToken }), [token, setToken]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const context = useContext(AuthContext);

  if (!context) {
    throw new Error("useAuth must be used within an AuthProvider");
  }

  return context;
}
