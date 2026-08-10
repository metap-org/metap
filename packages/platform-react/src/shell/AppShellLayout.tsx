import type { ReactNode } from "react";
import { AppShell, Anchor, Badge, Button, Group, Text } from "@mantine/core";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth/AuthContext";
import { useHasRole } from "../auth/Can";
import { useCurrentUser } from "../auth/useCurrentUser";
import { LocaleSwitcher } from "../i18n/LocaleSwitcher";
import { useNavigationAdapter } from "../navigation/NavigationContext";

export type ShellNavItem = {
  to: string;
  label: string;
  /** Hidden unless the current user holds one of these roles; visible to everyone if omitted. */
  roles?: string[];
};

function NavLink({ item }: { item: ShellNavItem }) {
  const navAdapter = useNavigationAdapter();
  const allowedByRole = useHasRole(item.roles ?? []);

  if (item.roles && !allowedByRole) {
    return null;
  }

  return (
    <Anchor component={navAdapter.Link} to={item.to} size="sm">
      {item.label}
    </Anchor>
  );
}

/** The shared page chrome every `platform-react` consumer app assembles its authenticated
 * routes into, instead of hand-rolling header/nav per app (`docs/roadmap.md` Phase 15). */
export function AppShellLayout({
  brand,
  navItems,
  children,
}: {
  brand: string;
  navItems: ShellNavItem[];
  children: ReactNode;
}) {
  const { t } = useTranslation();
  const { setToken } = useAuth();
  const { data: user } = useCurrentUser();
  const navAdapter = useNavigationAdapter();

  function handleLogout() {
    setToken(null);
    navAdapter.navigate(navAdapter.toLogin());
  }

  return (
    <AppShell header={{ height: 60 }} padding="md">
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between" wrap="nowrap">
          <Group gap="lg" wrap="nowrap">
            <Anchor component={navAdapter.Link} to={navAdapter.toHome()} fw={700} underline="never">
              {brand}
            </Anchor>
            <Group gap="md" wrap="nowrap">
              {navItems.map((item) => (
                <NavLink key={item.to} item={item} />
              ))}
            </Group>
          </Group>
          <Group gap="sm" wrap="nowrap">
            <LocaleSwitcher />
            {user ? (
              <Group gap={4} wrap="nowrap">
                {user.roles.map((role) => (
                  <Badge key={role} size="sm" variant="light">
                    {role}
                  </Badge>
                ))}
              </Group>
            ) : (
              <Text size="sm" c="dimmed" />
            )}
            <Button variant="subtle" size="compact-sm" onClick={handleLogout}>
              {t("shell.logout")}
            </Button>
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Main>{children}</AppShell.Main>
    </AppShell>
  );
}
