import type { ReactNode } from "react";
import { Navigate, Route, Routes, useNavigate, useParams } from "react-router-dom";
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
  OidcCallbackPage,
} from "@metap/platform-react";
import type { ShellNavItem } from "@metap/platform-react";
import { LoginPage } from "./demo/LoginPage";
import { DashboardPage } from "./pages/DashboardPage";
import { BoardPage } from "./pages/BoardPage";
import { BacklogPage } from "./pages/BacklogPage";
import { IssueDetailPage } from "./pages/IssueDetailPage";

// No `LowCodeEntitiesAdminPage`/nav item here, unlike crm-fe — jira-server's `main.rs`
// deliberately doesn't merge `metap_lowcode_http`'s router (this PoC doesn't need the low-code
// admin API), so that page's routes 404 against this backend.
function RequireAuth({ children }: { children: ReactNode }) {
  const { token } = useAuth();

  if (!token) {
    return <Navigate to="/login" replace />;
  }

  const navItems: ShellNavItem[] = [
    { to: "/", label: "Dashboard" },
    { to: "/board", label: "Board" },
    { to: "/backlog", label: "Backlog" },
    { to: "/records/jira.projects", label: "Projects" },
    { to: "/records/jira.sprints", label: "Sprints" },
    { to: "/records/jira.issues", label: "Issues" },
    { to: "/admin/users", label: "Users", roles: ["admin"] },
    { to: "/admin/policies", label: "Policies", roles: ["admin"] },
    { to: "/admin/cron-jobs", label: "Cron Jobs", roles: ["admin"] },
  ];

  return (
    <AppShellLayout brand="Jira (metap demo)" navItems={navItems}>
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
  const { entityName } = useParams<{ entityName: string }>();
  if (!entityName) return <div>Missing entity name</div>;
  return <GeneratedList entityName={entityName} />;
}

function NewRecordRoute() {
  const { entityName } = useParams<{ entityName: string }>();
  const navigate = useNavigate();
  if (!entityName) return <div>Missing entity name</div>;
  return (
    <GeneratedForm entityName={entityName} onSaved={() => navigate(`/records/${entityName}`)} />
  );
}

function RecordDetailRoute() {
  const { entityName, id } = useParams<{ entityName: string; id: string }>();
  if (!entityName || !id) return <div>Missing entity or id</div>;
  return <RecordDetail entityName={entityName} id={id} />;
}

function EditRecordRoute() {
  const { entityName, id } = useParams<{ entityName: string; id: string }>();
  const navigate = useNavigate();
  if (!entityName || !id) return <div>Missing entity or id</div>;
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
                <DashboardPage />
              </RequireAuth>
            }
          />
          <Route
            path="/board"
            element={
              <RequireAuth>
                <BoardPage />
              </RequireAuth>
            }
          />
          <Route
            path="/backlog"
            element={
              <RequireAuth>
                <BacklogPage />
              </RequireAuth>
            }
          />
          <Route
            path="/issues/:id"
            element={
              <RequireAuth>
                <IssueDetailPage />
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
        </Routes>
      </LocaleProvider>
    </AuthProvider>
  );
}
