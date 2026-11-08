import type { ReactNode } from 'react';

export type WorkspaceSection = 'events' | 'releases' | 'sources' | 'analysis' | 'saved-analyses';

const paths: Record<WorkspaceSection, ReactNode> = {
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
