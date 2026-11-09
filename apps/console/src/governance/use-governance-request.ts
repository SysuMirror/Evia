import { useCallback, useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { ApiError, type Project, type Session } from '@evia/api-client';
import { useCatalogScope } from '../catalog-scope';

export type GovernancePerform = <T>(
  operation: string,
  request: (signal: AbortSignal) => Promise<T>,
  success: (result: T) => void,
  failure?: (error: Error) => void,
) => Promise<void>;

/**
 * One mutation owns the request slot. Completions are accepted only while the
 * same session, project permissions and identity are current. Authorization
 * failures move the workspace to its denial path instead of surfacing as edits.
 */
export function useGovernanceRequest(
  project: Project,
  session: Session,
  identity: string,
  allowed: boolean,
) {
  const cache = useQueryClient();
  const [busy, setBusy] = useState('');
  const [denied, setDenied] = useState<Error>();
  const slot = useRef<AbortController | null>(null);
  const scope = useCatalogScope(identity, allowed && !denied, { project, session });

  useEffect(
    () => () => {
      slot.current?.abort();
    },
    [],
  );

  const deny = useCallback(
    (error: Error) => {
      setDenied(error);
      scope.invalidate();
      slot.current?.abort();
      setBusy('');
      void cache.cancelQueries({ queryKey: ['governance', project.id] });
      cache.removeQueries({ queryKey: ['governance', project.id] });
    },
    [scope.invalidate, cache, project.id],
  );

  const perform: GovernancePerform = useCallback(
    async (operation, request, success, failure) => {
      if (slot.current || denied) return;
      const controller = new AbortController();
      slot.current = controller;
      const token = scope.capture();
      setBusy(operation);
      try {
        const result = await request(controller.signal);
        if (!controller.signal.aborted && scope.isCurrent(token)) success(result);
      } catch (caught) {
        if (!controller.signal.aborted && scope.isCurrent(token)) {
          const error = caught instanceof Error ? caught : new Error('请求失败，请重试。');
          if (error instanceof ApiError && [401, 403].includes(error.status)) deny(error);
          else failure?.(error);
        }
      } finally {
        if (slot.current === controller) {
          slot.current = null;
          if (scope.isCurrent(token)) setBusy('');
        }
      }
    },
    [denied, scope],
  );

  return { perform, busy, denied, deny };
}
