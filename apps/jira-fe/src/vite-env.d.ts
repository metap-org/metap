/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_JIRA_TENANT_ID: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
