import type { ReactNode } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import { AuthProvider, useAuth } from "./platform/auth/AuthContext";
import { DevLoginPage } from "./demo/DevLoginPage";
import { EntitiesPage } from "./demo/EntitiesPage";
import { CustomersPage } from "./demo/CustomersPage";

function RequireAuth({ children }: { children: ReactNode }) {
  const { token } = useAuth();

  if (!token) {
    return <Navigate to="/dev-login" replace />;
  }

  return <>{children}</>;
}

export default function App() {
  return (
    <AuthProvider>
      <Routes>
        <Route path="/dev-login" element={<DevLoginPage />} />
        <Route
          path="/"
          element={
            <RequireAuth>
              <EntitiesPage />
            </RequireAuth>
          }
        />
        <Route
          path="/customers"
          element={
            <RequireAuth>
              <CustomersPage />
            </RequireAuth>
          }
        />
      </Routes>
    </AuthProvider>
  );
}
