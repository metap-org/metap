import type { ReactNode } from "react";
import { Navigate, Route, Routes, useNavigate, useParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  AppShellLayout,
  AuthProvider,
  Can,
  LocaleProvider,
  useAuth,
  RecordDetail,
  GeneratedForm,
  GeneratedList,
  UsersAdminPage,
  PoliciesAdminPage,
  CronJobsAdminPage,
  LowCodeEntitiesAdminPage,
  OidcCallbackPage,
} from "@metap/platform-react";
import type { ShellNavItem } from "@metap/platform-react";
import { LoginPage } from "./demo/LoginPage";
import { EntitiesPage } from "./demo/EntitiesPage";

function RequireAuth({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const { token } = useAuth();

  if (!token) {
    return <Navigate to="/login" replace />;
  }

  const navItems: ShellNavItem[] = [
    { to: "/", label: t("shell.navEntities") },
    { to: "/admin/users", label: t("shell.navUsers"), roles: ["admin"] },
    { to: "/admin/policies", label: t("shell.navPolicies"), roles: ["admin"] },
    { to: "/admin/cron-jobs", label: t("shell.navCronJobs"), roles: ["admin"] },
    { to: "/admin/lowcode", label: t("shell.navLowCode"), roles: ["admin"] },
  ];

  return (
    <AppShellLayout brand="Metap" navItems={navItems}>
      {children}
    </AppShellLayout>
  );
}

function RequireAdmin({ children }: { children: ReactNode }) {
  return (
    <Can roles={["admin"]} fallback={<Navigate to="/" replace />}>
      {children}
    </Can>
  );
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
          <Route path="/auth/oidc/callback" element={<OidcCallbackPage />} />
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
          <Route
            path="/admin/users"
            element={
              <RequireAuth>
                <RequireAdmin>
                  <UsersAdminPage />
                </RequireAdmin>
              </RequireAuth>
            }
          />
          <Route
            path="/admin/policies"
            element={
              <RequireAuth>
                <RequireAdmin>
                  <PoliciesAdminPage />
                </RequireAdmin>
              </RequireAuth>
            }
          />
          <Route
            path="/admin/cron-jobs"
            element={
              <RequireAuth>
                <RequireAdmin>
                  <CronJobsAdminPage />
                </RequireAdmin>
              </RequireAuth>
            }
          />
          <Route
            path="/admin/lowcode"
            element={
              <RequireAuth>
                <RequireAdmin>
                  <LowCodeEntitiesAdminPage />
                </RequireAdmin>
              </RequireAuth>
            }
          />
        </Routes>
      </LocaleProvider>
    </AuthProvider>
  );
}
