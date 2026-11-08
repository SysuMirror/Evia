import type {
  QueryJob,
  QueryResult,
  SavedAnalysisExecute,
  SavedAnalysisVersion,
} from '@evia/api-client';

export type SavedRunWork = { dirty: boolean; busy: boolean; retryPending?: boolean };
export type SavedRunRange = { from: string; to: string; timezone: string };
export type ExecutionAttempt = {
  key: string;
  version: SavedAnalysisVersion;
  body: SavedAnalysisExecute;
};
export type ExecutionSnapshot = ExecutionAttempt & { job: QueryJob; result?: QueryResult };
export const activeSavedJob = (job?: QueryJob) =>
  job?.state === 'queued' || job?.state === 'running';
