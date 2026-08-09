import type { ReactNode } from "react";
import { Navigate, Route, Routes, useNavigate, useParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  AuthProvider,
  LocaleProvider,
  useAuth,
  RecordDetail,
  GeneratedForm,
  GeneratedList,
} from "@metap/platform-react";
import { LoginPage } from "./demo/LoginPage";
import { EntitiesPage } from "./demo/EntitiesPage";

function RequireAuth({ children }: { children: ReactNode }) {
  const { token } = useAuth();

  if (!token) {
    return <Navigate to="/login" replace />;
  }

  return <>{children}</>;
}

function RecordsRoute() {
  const { t } = useTranslation();
  const { entityName } = useParams<{ entityName: string }>();

  if (!entityName) {
    return <div>{t("common.missingEntityName")}</div>;
  }

  return <GeneratedList entityName={entityName} />;
}

function NewRecordRoute() {
  const { t } = useTranslation();
  const { entityName } = useParams<{ entityName: string }>();
  const navigate = useNavigate();

  if (!entityName) {
    return <div>{t("common.missingEntityName")}</div>;
  }

  return (
    <GeneratedForm entityName={entityName} onSaved={() => navigate(`/records/${entityName}`)} />
  );
}

function RecordDetailRoute() {
  const { t } = useTranslation();
  const { entityName, id } = useParams<{ entityName: string; id: string }>();

  if (!entityName || !id) {
    return <div>{t("common.missingEntityOrId")}</div>;
  }

  return <RecordDetail entityName={entityName} id={id} />;
}

function EditRecordRoute() {
  const { t } = useTranslation();
  const { entityName, id } = useParams<{ entityName: string; id: string }>();
  const navigate = useNavigate();

  if (!entityName || !id) {
    return <div>{t("common.missingEntityOrId")}</div>;
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
      <LocaleProvider>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
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
      </LocaleProvider>
    </AuthProvider>
  );
}
