import {
  ApiError,
  type Environment,
  type SavedAnalysisCreate,
  type SavedAnalysisDefinition,
  type SavedAnalysisDraft,
  type SavedAnalysisPublish,
} from '@evia/api-client';

export type SavedAnalysisWorkspace = {
  scope: string;
  draft?: SavedAnalysisDraft;
  definition: SavedAnalysisDefinition;
};
export type CreateAttempt = { key: string; body: SavedAnalysisCreate };
export type PublishAttempt = { key: string; analysisID: string; body: SavedAnalysisPublish };
export type ScopedRequest = <T>(
  label: string,
  action: (signal: AbortSignal) => Promise<T>,
  success: (value: T) => void,
  failure?: (error: Error) => void,
) => Promise<void>;

export const savedAnalysesKey = (projectID: string, environment: Environment, actorID: string) =>
  ['saved-analyses', projectID, environment, actorID] as const;
export const uncertain = (error: unknown) =>
  error instanceof ApiError && (error.status === 0 || error.status >= 500 || error.status === 408);
export const revisionConflict = (error: unknown) =>
  error instanceof ApiError &&
  [
    'SAVED_ANALYSIS_REVISION_CONFLICT',
    'PUBLISHED_VERSION_CONFLICT',
    'VALIDATION_HASH_CONFLICT',
  ].includes(error.code);
