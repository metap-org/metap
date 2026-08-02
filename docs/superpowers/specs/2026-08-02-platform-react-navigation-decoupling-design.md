# Decouple Navigation from `packages/platform-react`

Date: 2026-08-02

Status: approved

Scope: first of an ordered pair of frontend sub-projects (this one; the second — a reference-field entity picker — is scoped but deliberately not designed in depth here, see "Related, not this sub-project" below).

## Motivation

`packages/platform-react`'s stated design goal (`docs/architectures/04-strategy.md`'s "Frontend Platform Package" section) is staying agnostic about how a consumer is built — no baked-in assumptions a micro-frontend or differently-structured monorepo consumer couldn't work around. `docs/architectures/11-risks.md` already tracks a violation of that goal, but investigating it precisely (not just trusting the risk entry) found the real shape is narrower and different than documented:

- Only 3 files actually import `react-router-dom`: `ApiErrorMessage.tsx` (`Link`), `GeneratedList.tsx` (`Link`), `RecordDetail.tsx` (`Link`, `useNavigate`) — 5 usages total. `GeneratedForm` and `WorkflowActionBar` (both named in the risk entry) don't import router at all — `GeneratedForm` already takes an `onSaved` callback prop and lets the caller navigate; `WorkflowActionBar` never navigates.
- The coupling is two layers, not one: every usage hardcodes both react-router's API *and* a specific URL scheme — `/records/${entityName}`, `/records/${entityName}/new`, `/records/${entityName}/${id}`, `/records/${entityName}/${id}/edit`, `/dev-login`. That URL scheme is `apps/demo`'s own routing convention, not something `packages/platform-react` has any business assuming. A consumer using react-router *with a different URL scheme* couldn't use these components as-is either — swapping only the router library wouldn't have been enough.

## Design

New `packages/platform-react/src/navigation/` module: a `NavigationAdapter` interface + React Context, injected by the consumer, that abstracts both concerns at once (router library and URL scheme) behind the five concrete actions these components actually need — no more, no less:

```ts
export type NavigationAdapter = {
  toRecordList(entityName: string): string;
  toNewRecord(entityName: string): string;
  toRecordDetail(entityName: string, id: string): string;
  toEditRecord(entityName: string, id: string): string;
  toLogin(): string;
  navigate(path: string): void;
  Link: React.ComponentType<{ to: string; children: React.ReactNode }>;
};

export const NavigationContext = createContext<NavigationAdapter | null>(null);

export function useNavigationAdapter(): NavigationAdapter {
  const adapter = useContext(NavigationContext);
  if (!adapter) {
    throw new Error(
      "useNavigationAdapter() called with no NavigationContext.Provider above it — every packages/platform-react consumer must provide one.",
    );
  }
  return adapter;
}
```

Failing loudly (not silently falling back to some default path scheme) if no provider is set matches this session's established stance elsewhere (`PermissionService.scopedTenant`'s recent fix) — a missing adapter is a real integration bug, not something to paper over.

`ApiErrorMessage`/`GeneratedList`/`RecordDetail` are rewritten to call `useNavigationAdapter()` and use `adapter.Link`/`adapter.navigate`/`adapter.toX(...)` instead of importing `react-router-dom` or hardcoding a path template directly. `react-router-dom` moves out of these files entirely — it stays a `peerDependency` of the package overall only because *the adapter implementation* (below) needs it, not because the generated-UI components do anymore.

**`apps/demo` gets a small, concrete `react-router-dom`-backed adapter** — `apps/demo/src/reactRouterNavigationAdapter.tsx` (lives in the app, not the package, since it encodes `apps/demo`'s own URL scheme):

```tsx
export function reactRouterAdapter(): NavigationAdapter {
  return {
    toRecordList: (entityName) => `/records/${entityName}`,
    toNewRecord: (entityName) => `/records/${entityName}/new`,
    toRecordDetail: (entityName, id) => `/records/${entityName}/${id}`,
    toEditRecord: (entityName, id) => `/records/${entityName}/${id}/edit`,
    toLogin: () => "/dev-login",
    navigate: (path) => { /* wraps react-router's navigate() via a small hook-to-function bridge */ },
    Link: RouterLink, // react-router-dom's Link, re-exported
  };
}
```

`apps/demo/src/main.tsx` wraps `<App />` in `<NavigationContext.Provider value={...}>` alongside its existing `BrowserRouter`. This is the "recipe" a future differently-built consumer (different router, different URL scheme, or a micro-frontend host) copies and adapts — not a new generalized `PlatformShell` component. `QueryClientProvider`/`MantineProvider`/`Notifications` stay exactly as they are today (already correctly consumer-owned via `peerDependencies` — that part of the "shell" concern isn't actually broken, only navigation was).

## Related, not this sub-project

Investigating "what components are missing" surfaced one concrete, real gap worth naming now and designing later: `FieldInput`'s `"reference"` field kind (`EntityField.kind === "reference"`, carrying `refEntity: string` — which entity it points at) renders as a bare `TextInput` today — free-text entry, not an actual entity picker, despite the metadata already declaring exactly what it should pick from. This is real, half-finished functionality, not speculative work — but designing it properly needs a new decision this sub-project doesn't make: `EntityDefinition` has no concept of a "display field" (which field of the referenced entity is the human-readable label — e.g. show a customer's `name`, not its UUID), so a reference picker needs that metadata convention added first, then flowed through the OpenAPI-generated contract (sub-project 5's pipeline) before the picker component itself can be built. Scoped as the next frontend sub-project, not designed further here.

Other "more components" candidates surfaced but not prioritized ahead of the reference picker: bulk row actions (multi-select + bulk delete/transition on `GeneratedList`), a shared empty-state/error-boundary component. Neither has the same "half-finished, metadata already supports it" urgency the reference picker does.

## Testing

`packages/platform-react` has no test framework (established boundary this session) — verification is `pnpm typecheck`/`pnpm build` across `packages/platform-react` and `apps/demo` (proves the adapter's shape satisfies every consumer, and `apps/demo`'s concrete implementation satisfies the interface) + `pnpm lint`. Manual browser check (same known sandbox limitation as every other frontend sub-project this session — no working headless Chromium) covers: New/View/Edit navigation from the list and detail pages, the 401 "sign in again" link, and a workflow-transition-then-navigate flow, all still working identically to before the refactor.

## Out of scope

- The reference-field entity picker itself (see above) — next sub-project, needs its own metadata-model decision first.
- A generalized `PlatformShell` component bundling Query/Mantine/Navigation — not justified with only one real consumer; the `apps/demo` adapter file *is* the reference recipe.
- Publishing `@metap/platform-react` to a registry — unrelated, separate, already-documented trigger.
