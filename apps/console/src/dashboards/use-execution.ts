import { useEffect, useRef, useState } from 'react';
import {
  api,
  ApiError,
  type DashboardExecute,
  type DashboardRun,
  type DashboardVersion,
  type QueryResult,
  type SavedAnalysisVersion,
} from '@evia/api-client';
import { useCatalogScope } from '../catalog-scope';
import type { CatalogPageProps } from '../catalog';
import { uncertain } from './types';
import { retainDashboardProgress } from './run-progress';

export type DashboardExecutionSnapshot = {
  key: string;
  version: DashboardVersion;
  body: DashboardExecute;
  analyses: Map<string, SavedAnalysisVersion>;
  run: DashboardRun;
  results: Record<string, QueryResult>;
  resultErrors: Record<string, Error>;
};
type Attempt = Omit<DashboardExecutionSnapshot, 'run' | 'results' | 'resultErrors'>;
export const activeDashboardRun = (run?: DashboardRun) =>
  run?.widgets.some((w) => w.query.state === 'queued' || w.query.state === 'running') ?? false;
const activeQuery = (state: string) => state === 'queued' || state === 'running';
export function useDashboardExecution({
  context,
  version,
  analyses,
  disabled,
  body,
  onInvalid,
  onExecuted,
  onDenied,
}: {
  context: CatalogPageProps;
  version: DashboardVersion;
  analyses: Map<string, SavedAnalysisVersion>;
  disabled: boolean;
  body: () => DashboardExecute;
  onInvalid: (error: string) => void;
  onExecuted: (body: DashboardExecute) => void;
  onDenied: (error: Error) => void;
}) {
  const { project, environment, session } = context;
  const identity = JSON.stringify([
    project.id,
    environment,
    session.user_id,
    session.csrf_token,
    [...project.permissions].sort(),
    version.dashboard_id,
  ]);
  const [pollGeneration, setPollGeneration] = useState(0);
  const [attempt, setAttempt] = useState<Attempt>(),
    [snapshot, setSnapshot] = useState<DashboardExecutionSnapshot>(),
    [error, setError] = useState<Error | null>(null),
    [notice, setNotice] = useState(''),
    [busy, setBusy] = useState(false),
    [observing, setObserving] = useState(false),
    [denied, setDenied] = useState(false);
  const expiryWindow = useRef<{ runID: string; until: number } | undefined>(undefined);
  const scope = useCatalogScope(identity, !denied, { project, session }),
    lock = useRef(false),
    request = useRef<AbortController | null>(null),
    poll = useRef<AbortController | null>(null);

  useEffect(() => {
    setAttempt(undefined);
    setSnapshot(undefined);
    setError(null);
    setNotice('');
    setBusy(false);
    setObserving(false);
    setDenied(false);
    lock.current = false;
    return () => {
      request.current?.abort();
      poll.current?.abort();
    };
  }, [identity]);
  function fail(caught: unknown) {
    const failure = caught instanceof Error ? caught : new Error('请求失败，请重试。');
    setError(failure);
    if (failure instanceof ApiError && [401, 403].includes(failure.status)) {
      scope.invalidate();
      request.current?.abort();
      poll.current?.abort();
      setSnapshot(undefined);
      setAttempt(undefined);
      setDenied(true);
      setObserving(false);
      setBusy(false);
      lock.current = false;
      onDenied(failure);
    }
    return failure;
  }
  async function execute() {
    if (disabled || denied || lock.current || activeDashboardRun(snapshot?.run)) return;
    let next = attempt;
    if (!next) {
      try {
        next = {
          key: crypto.randomUUID(),
          version: structuredClone(version),
          body: body(),
          analyses: structuredClone(analyses),
        };
      } catch (caught) {
        onInvalid((caught as Error).message);
        return;
      }
    }
    const token = scope.capture(),
      controller = new AbortController();
    request.current = controller;
    lock.current = true;
    setBusy(true);
    setError(null);
    setNotice('');
    setAttempt(next);
    try {
      const run = await api.executeDashboardVersion(
        project.id,
        next.version.dashboard_id,
        next.version.version,
        next.body,
        next.key,
        session.csrf_token,
        controller.signal,
      );
      if (!controller.signal.aborted && scope.isCurrent(token)) {
        setSnapshot({ ...next, run, results: {}, resultErrors: {} });
        setAttempt(undefined);
        onExecuted(next.body);
        setObserving(true);
      }
    } catch (caught) {
      if (!controller.signal.aborted && scope.isCurrent(token)) {
        fail(caught);
        if (!uncertain(caught)) setAttempt(undefined);
      }
    } finally {
      if (!controller.signal.aborted && scope.isCurrent(token)) {
        lock.current = false;
        setBusy(false);
      }
    }
  }
  async function cancel(widgetID?: string) {
    if (lock.current || denied || !snapshot) return;
    const token = scope.capture(),
      controller = new AbortController(),
      runID = snapshot.run.run_id;
    request.current = controller;
    lock.current = true;
    setBusy(true);
    setError(null);
    poll.current?.abort();
    setObserving(false);
    try {
      if (widgetID) {
        const widget = snapshot.run.widgets.find((w) => w.widget_id === widgetID);
        if (widget) {
          const query = await api.cancelQuery(
            project.id,
            widget.query.query_id,
            session.csrf_token,
            controller.signal,
          );
          if (!controller.signal.aborted && scope.isCurrent(token))
            setSnapshot((previous) => {
              if (previous?.run.run_id !== runID) return previous;
              const confirmed = {
                ...previous.run,
                widgets: previous.run.widgets.map((w) =>
                  w.widget_id === widgetID ? { ...w, query } : w,
                ),
              };
              return { ...previous, run: retainDashboardProgress(confirmed, previous.run) };
            });
        }
      } else {
        const run = await api.cancelDashboardRun(
          project.id,
          runID,
          session.csrf_token,
          controller.signal,
        );
        if (!controller.signal.aborted && scope.isCurrent(token))
          setSnapshot((previous) =>
            previous?.run.run_id === runID ? { ...previous, run } : previous,
          );
      }
      if (!controller.signal.aborted && scope.isCurrent(token)) {
        setNotice(widgetID ? '已提交组件取消请求。' : '已提交看板取消请求。');
        const run = await api.getDashboardRun(project.id, runID, controller.signal);
        if (!controller.signal.aborted && scope.isCurrent(token)) {
          setSnapshot((previous) =>
            previous?.run.run_id === runID ? { ...previous, run } : previous,
          );
          setPollGeneration((n) => n + 1);
          setObserving(true);
        }
      }
    } catch (caught) {
      if (!controller.signal.aborted && scope.isCurrent(token)) fail(caught);
    } finally {
      if (!controller.signal.aborted && scope.isCurrent(token)) {
        lock.current = false;
        setBusy(false);
      }
    }
  }
  useEffect(() => {
    if (!snapshot || !observing || denied) return;
    const runID = snapshot.run.run_id,
      controller = new AbortController(),
      token = scope.capture();
    poll.current = controller;
    const current = () => !controller.signal.aborted && scope.isCurrent(token);
    const wait = (ms: number) =>
      new Promise<void>((resolve, reject) => {
        const timer = window.setTimeout(() => {
          controller.signal.removeEventListener('abort', abort);
          resolve();
        }, ms);
        function abort() {
          clearTimeout(timer);
          reject(new DOMException('Aborted', 'AbortError'));
        }
        controller.signal.addEventListener('abort', abort, { once: true });
      });
    void (async () => {
      let local = snapshot;
      const deadline = Date.now() + 12 * 60 * 1000;
      try {
        while (current()) {
          const refreshed = await api.getDashboardRun(project.id, runID, controller.signal);
          if (!current()) return;
          local = { ...local, run: refreshed };
          let expiredDuringRead = false;
          const results = { ...local.results },
            resultErrors = { ...local.resultErrors };
          await Promise.all(
            local.run.widgets.map(async (widget) => {
              const query = widget.query;
              if (query.state !== 'succeeded') {
                delete results[widget.widget_id];
                return;
              }
              if (results[widget.widget_id]) return;
              try {
                results[widget.widget_id] = await api.getQueryResult(
                  project.id,
                  query.query_id,
                  controller.signal,
                );
                delete resultErrors[widget.widget_id];
              } catch (caught) {
                if (caught instanceof ApiError && [401, 403].includes(caught.status)) throw caught;
                resultErrors[widget.widget_id] =
                  caught instanceof Error ? caught : new Error('结果读取失败，请重试。');
                if (caught instanceof ApiError && caught.code === 'QUERY_EXPIRED') {
                  widget.query = { ...query, state: 'expired' };
                  expiredDuringRead = true;
                }
              }
            }),
          );
          if (!current()) return;
          if (expiredDuringRead) {
            const run = await api.getDashboardRun(project.id, runID, controller.signal);
            if (!current()) return;
            local = { ...local, run };
          }
          local = { ...local, results, resultErrors };
          setSnapshot((previous) =>
            previous?.run.run_id === runID
              ? { ...local, run: retainDashboardProgress(previous.run, local.run) }
              : previous,
          );
          if (!activeDashboardRun(local.run)) {
            setObserving(false);
            return;
          }
          if (Date.now() >= deadline) {
            setObserving(false);
            setNotice('已暂停自动检查，服务端可能仍在执行，可继续检查或取消。');
            return;
          }
          await wait(Math.min(10000, Math.max(250, local.run.poll_after_ms ?? 1000)));
          if (!current()) return;
        }
      } catch (caught) {
        if (current()) {
          fail(caught);
          setObserving(false);
        }
      }
    })();
    return () => controller.abort();
  }, [snapshot?.run.run_id, observing, identity, denied, pollGeneration]);
  // Re-read child states at the earliest run expiry, retaining every still-readable sibling.
  useEffect(() => {
    if (!snapshot || !Object.keys(snapshot.results).length || observing) return;
    const expiry = Date.parse(snapshot.run.expires_at),
      token = scope.capture();
    if (!Number.isFinite(expiry)) return;
    if (expiry <= Date.now()) {
      if (expiryWindow.current?.runID !== snapshot.run.run_id)
        expiryWindow.current = { runID: snapshot.run.run_id, until: Date.now() + 12 * 60 * 1000 };
      if (Date.now() >= expiryWindow.current.until) {
        setNotice('已暂停结果有效期检查，以下为最后确认的组件状态，可继续检查。');
        return;
      }
    }
    const delay = expiry > Date.now() ? Math.max(1, expiry - Date.now()) : 60000;
    const timer = window.setTimeout(
      () => {
        if (scope.isCurrent(token)) setObserving(true);
      },
      Math.min(2147483647, delay),
    );
    return () => clearTimeout(timer);
  }, [snapshot, observing, identity]);
  function resume() {
    expiryWindow.current = undefined;
    setError(null);
    setObserving(true);
  }
  return { attempt, snapshot, error, notice, busy, observing, denied, execute, cancel, resume };
}
