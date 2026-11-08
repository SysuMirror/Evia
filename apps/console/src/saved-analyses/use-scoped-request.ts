import { useCallback, useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { ApiError } from '@evia/api-client';
import type { CatalogPageProps } from '../catalog';
import { useCatalogScope } from '../catalog-scope';
import { savedAnalysesKey, type ScopedRequest } from './types';

/** One mutation owns the request slot until its current authorized scope completes it. */
export function useSavedAnalysisRequest(
  context: CatalogPageProps,
  identity: string,
  canRead: boolean,
) {
  const { project, environment, session } = context;
  const cache = useQueryClient();
  const [busy, setBusy] = useState('');
  const [denied, setDenied] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const request = useRef<AbortController | null>(null);
  const operation = useRef(false);
  const scope = useCatalogScope(identity, canRead && !denied, { project, session });
  useEffect(() => {
    setBusy('');
    setDenied(false);
    setError(null);
    operation.current = false;
    return () => request.current?.abort();
  }, [identity]);
  const deny = useCallback(
    (failure: Error) => {
      scope.invalidate();
      request.current?.abort();
      setDenied(true);
      setError(failure);
      setBusy('');
      operation.current = false;
      const queryKey = savedAnalysesKey(project.id, environment, session.user_id);
      void cache.cancelQueries({ queryKey });
      cache.removeQueries({ queryKey });
    },
    [scope.invalidate, cache, project.id, environment, session.user_id],
  );
  const perform: ScopedRequest = async (label, action, success, failure) => {
    if (operation.current || denied) return;
    operation.current = true;
    const token = scope.capture(),
      controller = new AbortController();
    request.current = controller;
    setBusy(label);
    setError(null);
    try {
      const value = await action(controller.signal);
      if (!controller.signal.aborted && scope.isCurrent(token)) success(value);
    } catch (caught) {
      if (!controller.signal.aborted && scope.isCurrent(token)) {
        const next = caught instanceof Error ? caught : new Error('请求失败，请重试。');
        if (next instanceof ApiError && [401, 403].includes(next.status)) deny(next);
        else {
          setError(next);
          failure?.(next);
        }
      }
    } finally {
      if (!controller.signal.aborted && scope.isCurrent(token)) {
        setBusy('');
        operation.current = false;
      }
    }
  };
  const isPending = useCallback(() => operation.current, []);
  return { busy, denied, error, setError, deny, perform, isPending };
}
