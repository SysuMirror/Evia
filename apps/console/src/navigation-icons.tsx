import type { ReactNode } from 'react';

export type WorkspaceSection =
  | 'events'
  | 'releases'
  | 'sources'
  | 'diagnostics'
  | 'analysis'
  | 'saved-analyses'
  | 'dashboards'
  | 'reports'
  | 'machine-access'
  | 'governance'
  | 'identity'
  | 'session-replay'
  | 'erasure'
  | 'exports'
  | 'retention'
  | 'archiving'
  | 'metrics'
  | 'formula-metrics';

const paths: Record<WorkspaceSection, ReactNode> = {
  exports: (
    <>
      <path d="M12 3v12m0 0l-4-4m4 4l4-4" />
      <path d="M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2" />
    </>
  ),
  retention: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 7.5V12l3 2" />
    </>
  ),
  archiving: (
    <>
      <rect x="4" y="4" width="16" height="5" rx="1.5" />
      <path d="M5 9v10a1.5 1.5 0 0 0 1.5 1.5h11A1.5 1.5 0 0 0 19 19V9M12 12v7m0 0l-3-3m3 3l3-3" />
    </>
  ),
  erasure: (
    <>
      <path d="M4 7h16M9 7V5a2 2 0 0 1 2-2h2a2 2 0 0 1 2 2v2M6 7l1 13a2 2 0 0 0 2 1.8h6a2 2 0 0 0 2-1.8L18 7M10 11v6M14 11v6" />
    </>
  ),
  'formula-metrics': (
    <>
      <path d="M4 7h7M15 7h5M4 17h5M13 17h7" />
      <circle cx="13" cy="7" r="2.2" />
      <circle cx="11" cy="17" r="2.2" />
    </>
  ),
  governance: (
    <>
      <circle cx="12" cy="8" r="3.2" />
      <path d="M5.5 19a6.5 6.5 0 0 1 13 0M4 21h16" />
    </>
  ),
  identity: (
    <>
      <circle cx="8.5" cy="8" r="3" />
      <circle cx="15.5" cy="8" r="3" />
      <path d="M3 19a5.5 5.5 0 0 1 11 0M10 19a5.5 5.5 0 0 1 11 0" />
    </>
  ),
  'session-replay': (
    <>
      <rect x="3" y="4" width="18" height="14" rx="3" />
      <path d="m10 8 5 3-5 3V8ZM8 21h8" />
    </>
  ),
  metrics: (
    <>
      <path d="M4 20V10M10 20V4M16 20v-7M22 20H2" />
    </>
  ),
  'machine-access': (
    <>
      <rect x="4" y="4" width="16" height="12" rx="3" />
      <path d="M9 20h6m-3-4v4M8 9h.01M12 9h4M8 12h8" />
    </>
  ),
  dashboards: (
    <>
      <rect x="3" y="3" width="11" height="8" rx="2" />
      <rect x="16" y="3" width="5" height="8" rx="2" />
      <rect x="3" y="13" width="7" height="8" rx="2" />
      <rect x="12" y="13" width="9" height="8" rx="2" />
    </>
  ),
  events: (
    <>
      <rect x="4" y="3.5" width="16" height="17" rx="3" />
      <path d="M8 8h8M8 12h8M8 16h5" />
    </>
  ),
  releases: (
    <>
      <path d="M12 15V3m-4 4 4-4 4 4M5 12v5a3 3 0 0 0 3 3h8a3 3 0 0 0 3-3v-5" />
    </>
  ),
  sources: (
    <>
      <rect x="8" y="8" width="8" height="8" rx="2" />
      <path d="M12 3v5m0 8v5M3 12h5m8 0h5M6 6l3 3m6 6 3 3M6 18l3-3m6-6 3-3" />
    </>
  ),
  diagnostics: (
    <>
      <path d="M3 12h4l2.5-6 4 12L16 12h5" />
    </>
  ),
  analysis: (
    <>
      <path d="M4 4v16h16M8 15l4-5 4 2 4-7" />
    </>
  ),
  'saved-analyses': (
    <>
      <path d="M6 3h12a2 2 0 0 1 2 2v16l-8-4-8 4V5a2 2 0 0 1 2-2Z" />
      <path d="M8 12V9m4 3V6m4 6v-2" />
    </>
  ),
  reports: (
    <>
      <path d="M7 3h10a2 2 0 0 1 2 2v16l-7-3-7 3V5a2 2 0 0 1 2-2Z" />
      <path d="M9.5 9h5M9.5 13h5M9.5 17h3" />
    </>
  ),
};

export function NavigationIcon({ section }: { section: WorkspaceSection }) {
  return (
    <svg
      width="20"
      height="20"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {paths[section]}
    </svg>
  );
}
