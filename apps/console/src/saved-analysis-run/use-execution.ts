import { useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type Project,
  type SavedAnalysisExecute,
  type SavedAnalysisVersion,
  type Session,
} from '@evia/api-client';
import { useCatalogScope } from '../catalog-scope';
import { activeSavedJob as active, type ExecutionAttempt, type ExecutionSnapshot } from './types';

const uncertain = (error: unknown) =>
  error instanceof ApiError && (error.status === 0 || error.status >= 500 || error.status === 408);

export function useSavedExecution({
  version,
  project,
  environment,
  session,
  disabled,
  rangeBody,
  onInvalidRange,
  onExecuted,
  onAccessDenied,
}: {
  version: SavedAnalysisVersion;
  project: Project;
  environment: Environment;
  session: Session;
  disabled: boolean;
  rangeBody: () => SavedAnalysisExecute;
  onInvalidRange: (message: string) => void;
  onExecuted: (body: SavedAnalysisExecute) => void;
  onAccessDenied: (error: Error) => void;
}) {
  const cache = useQueryClient();
  const [attempt, setAttempt] = useState<ExecutionAttempt>();
  const [snapshot, setSnapshot] = useState<ExecutionSnapshot>();
  const [error, setError] = useState<Error | null>(null);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [observing, setObserving] = useState(false);
  const [denied, setDenied] = useState(false);
  const operation = useRef(false);
  const request = useRef<AbortController | null>(null);
  const polling = useRef<AbortController | null>(null);
  const identity = JSON.stringify([
    project.id,
    environment,
    session.user_id,
    session.csrf_token,
    [...project.permissions].sort(),
    version.analysis_id,
  ]);
  const scope = useCatalogScope(identity, !denied, { project, session });
  useEffect(() => {
    setSnapshot(undefined);
    setAttempt(undefined);
    setError(null);
    setNotice('');
    setDenied(false);
    setBusy(false);
    setObserving(false);
    operation.current = false;
    return () => {
      request.current?.abort();
      polling.current?.abort();
    };
  }, [identity]);
  function fail(failure: unknown) {
    const next = failure instanceof Error ? failure : new Error('请求失败，请重试。');
    if (failure instanceof ApiError && [401, 403].includes(failure.status)) {
      scope.invalidate();
      setDenied(true);
      setSnapshot(undefined);
      setAttempt(undefined);
      polling.current?.abort();
      setObserving(false);
      cache.removeQueries({
        queryKey: ['saved-analyses', project.id, environment, session.user_id],
      });
      onAccessDenied(next);
    }
    if (failure instanceof ApiError && failure.code === 'QUERY_EXPIRED') {
      setSnapshot((previous) =>
        previous
          ? { ...previous, job: { ...previous.job, state: 'expired' }, result: undefined }
          : previous,
      );
      setObserving(false);
    }
    setError(next);
  }
  async function execute() {
    if (
      disabled ||
      operation.current ||
      active(snapshot?.job) ||
      denied ||
      version.environment !== environment
    )
      return;
    let current = attempt;
    if (!current) {
      try {
        current = {
          key: crypto.randomUUID(),
          version: structuredClone(version),
          body: rangeBody(),
        };
      } catch (failure) {
        onInvalidRange((failure as Error).message);
        return;
      }
    }
    const token = scope.capture(),
      controller = new AbortController();
    operation.current = true;
    request.current = controller;
    setBusy(true);
    setError(null);
    setAttempt(current);
    setNotice('');
    try {
      const job = await api.executeSavedAnalysisVersion(
        project.id,
        current.version.analysis_id,
        current.version.version,
        current.body,
        current.key,
        session.csrf_token,
        controller.signal,
      );
      if (!scope.isCurrent(token) || controller.signal.aborted) return;
      polling.current?.abort();
      setSnapshot({ ...current, job });
      onExecuted(current.body);
      setAttempt(undefined);
      setObserving(true);
    } catch (failure) {
      if (scope.isCurrent(token) && !controller.signal.aborted) {
        if (!uncertain(failure)) setAttempt(undefined);
        fail(failure);
      }
    } finally {
      if (scope.isCurrent(token) && !controller.signal.aborted) {
        operation.current = false;
        setBusy(false);
      }
    }
  }
  async function cancel() {
    if (operation.current || !snapshot || !active(snapshot.job)) return;
    const token = scope.capture(),
      controller = new AbortController(),
      queryID = snapshot.job.query_id;
    operation.current = true;
    request.current = controller;
    polling.current?.abort();
    setObserving(false);
    setBusy(true);
    setError(null);
    try {
      const job = await api.cancelQuery(project.id, queryID, session.csrf_token, controller.signal);
      if (!scope.isCurrent(token) || controller.signal.aborted) return;
      setSnapshot((previous) =>
        previous?.job.query_id === queryID ? { ...previous, job } : previous,
      );
      setNotice(
        job.state === 'canceled' ? '查询已取消。' : '服务端已处理取消请求，以下状态以服务端为准。',
      );
      if (active(job) || job.state === 'succeeded') setObserving(true);
    } catch (failure) {
      if (scope.isCurrent(token) && !controller.signal.aborted) fail(failure);
    } finally {
      if (scope.isCurrent(token) && !controller.signal.aborted) {
        operation.current = false;
        setBusy(false);
      }
    }
  }
  useEffect(() => {
    if (!observing || !snapshot || denied) return;
    const controller = new AbortController(),
      token = scope.capture(),
      queryID = snapshot.job.query_id;
    polling.current = controller;
    const current = () => !controller.signal.aborted && scope.isCurrent(token);
    const wait = (milliseconds: number) =>
      new Promise<void>((resolve, reject) => {
        const abort = () => {
          clearTimeout(timer);
          reject(new DOMException('Aborted', 'AbortError'));
        };
        const timer = window.setTimeout(() => {
          controller.signal.removeEventListener('abort', abort);
          resolve();
        }, milliseconds);
        controller.signal.addEventListener('abort', abort, { once: true });
      });
    void (async () => {
      let job = snapshot.job;
      const deadline = Date.now() + 12 * 60 * 1000;
      try {
        while (current()) {
          if (job.state === 'succeeded') {
            const result = await api.getQueryResult(project.id, queryID, {
              signal: controller.signal,
            });
            if (current()) {
              setSnapshot((previous) =>
                previous?.job.query_id === queryID ? { ...previous, job, result } : previous,
              );
              setObserving(false);
            }
            return;
          }
          if (!active(job)) {
            if (current()) setObserving(false);
            return;
          }
          if (Date.now() >= deadline) {
            setObserving(false);
            setNotice('已暂停自动检查，服务端查询可能仍在执行，可继续检查或取消。');
            return;
          }
          await wait(Math.max(250, job.poll_after_ms ?? 1000));
          if (!current()) return;
          job = await api.getQuery(project.id, queryID, controller.signal);
          if (current())
            setSnapshot((previous) =>
              previous?.job.query_id === queryID ? { ...previous, job } : previous,
            );
        }
      } catch (failure) {
        if (current()) {
          fail(failure);
          setObserving(false);
        }
      }
    })();
    return () => controller.abort();
  }, [observing, snapshot?.job.query_id, identity, denied]);

  function resume() {
    setError(null);
    setObserving(true);
  }
  return {
    identity,
    attempt,
    snapshot,
    error,
    notice,
    busy,
    observing,
    denied,
    execute,
    cancel,
    resume,
  };
}
