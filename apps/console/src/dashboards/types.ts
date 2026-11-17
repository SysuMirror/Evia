import {
  ApiError,
  type Environment,
  type DashboardCreate,
  type DashboardDefinition,
  type DashboardDraft,
  type DashboardPublish,
  type DashboardRebinding,
  type MetricRecord,
  type MetricVersion,
  type SavedAnalysisVersion,
} from '@evia/api-client';

/** A picker selection that pins exactly one immutable widget source. */
export type DashboardWidgetSource =
  | { kind: 'analysis'; analysis: SavedAnalysisVersion }
  | { kind: 'metric'; metric: MetricRecord; version: MetricVersion }
  | { kind: 'text'; content: string };

/** Copy request carried from the modal through the list to the controller. */
export type DashboardCopyRequest = {
  title: string;
  folderID: string;
  targetEnvironment?: Environment;
  rebindings?: DashboardRebinding[];
};

export type DashboardWorkspace = {
  scope: string;
  draft?: DashboardDraft;
  definition: DashboardDefinition;
  /** Target folder for create/save; empty string means the project root. */
  folderID: string;
};
export type CreateAttempt = { key: string; body: DashboardCreate };
export type PublishAttempt = { key: string; dashboardID: string; body: DashboardPublish };
export type ScopedRequest = <T>(
  label: string,
  action: (signal: AbortSignal) => Promise<T>,
  success: (value: T) => void,
  failure?: (error: Error) => void,
) => Promise<void>;

export const dashboardsKey = (projectID: string, environment: Environment, actorID: string) =>
  ['dashboards', projectID, environment, actorID] as const;

/** List scope for the dashboard listing: '' = all, 'root' = unfiled, UUID = one folder. */
export type DashboardFolderScope = '' | 'root' | string;
export const uncertain = (error: unknown) =>
  error instanceof ApiError && (error.status === 0 || error.status >= 500 || error.status === 408);
export const revisionConflict = (error: unknown) =>
  error instanceof ApiError &&
  [
    'DASHBOARD_REVISION_CONFLICT',
    'PUBLISHED_VERSION_CONFLICT',
    'VALIDATION_HASH_CONFLICT',
  ].includes(error.code);
