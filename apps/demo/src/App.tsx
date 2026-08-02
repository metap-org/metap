import type { ReactNode } from "react";
import { Navigate, Route, Routes, useNavigate, useParams } from "react-router-dom";
import {
  AuthProvider,
  useAuth,
  RecordDetail,
  GeneratedForm,
  GeneratedList,
} from "@metap/platform-react";
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

function NewRecordRoute() {
  const { entityName } = useParams<{ entityName: string }>();
  const navigate = useNavigate();

  if (!entityName) {
    return <div>Missing entity name.</div>;
  }

  return (
    <GeneratedForm entityName={entityName} onSaved={() => navigate(`/records/${entityName}`)} />
  );
}

function RecordDetailRoute() {
  const { entityName, id } = useParams<{ entityName: string; id: string }>();

  if (!entityName || !id) {
    return <div>Missing entity name or id.</div>;
  }

  return <RecordDetail entityName={entityName} id={id} />;
}

function EditRecordRoute() {
  const { entityName, id } = useParams<{ entityName: string; id: string }>();
  const navigate = useNavigate();

  if (!entityName || !id) {
    return <div>Missing entity name or id.</div>;
  }

  return (
    <GeneratedForm
      entityName={entityName}
      recordId={id}
      onSaved={() => navigate(`/records/${entityName}/${id}`)}
    />
  );
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
        <Route
          path="/records/:entityName/new"
          element={
            <RequireAuth>
              <NewRecordRoute />
            </RequireAuth>
          }
        />
        <Route
          path="/records/:entityName/:id"
          element={
            <RequireAuth>
              <RecordDetailRoute />
            </RequireAuth>
          }
        />
        <Route
          path="/records/:entityName/:id/edit"
          element={
            <RequireAuth>
              <EditRecordRoute />
            </RequireAuth>
          }
        />
      </Routes>
    </AuthProvider>
  );
}
