import type { ReactNode } from "react";
import { Navigate, Route, Routes, useParams } from "react-router-dom";
import { AuthProvider, useAuth } from "./platform/auth/AuthContext";
import { GeneratedList } from "./platform/list/GeneratedList";
import { DevLoginPage } from "./demo/DevLoginPage";
import { EntitiesPage } from "./demo/EntitiesPage";

function RequireAuth({ children }: { children: ReactNode }) {
  const { token } = useAuth();

  if (!token) {
    return <Navigate to="/dev-login" replace />;
  }

  return <>{children}</>;
}

function RecordsRoute() {
  const { entityName } = useParams<{ entityName: string }>();

  if (!entityName) {
    return <div>Missing entity name.</div>;
  }

  return <GeneratedList entityName={entityName} />;
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
          path="/records/:entityName"
          element={
            <RequireAuth>
              <RecordsRoute />
            </RequireAuth>
          }
        />
      </Routes>
    </AuthProvider>
  );
}
