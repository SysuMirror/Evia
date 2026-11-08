import { useCallback, useLayoutEffect, useRef } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { type Project, type Session } from '@evia/api-client';

type ScopeToken = { generation: number; identity: string };
/** Async mutation completions may outlive both the component and its session. */
export function useCatalogScope(
  identity: string,
  enabled: boolean,
  authorization: { project: Project; session: Session },
) {
  const cache = useQueryClient();
  const current = useRef({ generation: 0, identity, enabled, mounted: false, authorization });
  current.current.identity = identity;
  current.current.enabled = enabled;
  current.current.authorization = authorization;
  useLayoutEffect(() => {
    current.current.generation += 1;
    current.current.mounted = true;
    return () => {
      current.current.generation += 1;
      current.current.mounted = false;
    };
  }, [identity, enabled]);
  const capture = useCallback(
    (): ScopeToken => ({
      generation: current.current.generation,
      identity: current.current.identity,
    }),
    [],
  );
  const isCurrent = useCallback(
    (token: ScopeToken | undefined) => {
      const value = current.current;
      if (
        !token ||
        !value.mounted ||
        !value.enabled ||
        value.generation !== token.generation ||
        value.identity !== token.identity
      )
        return false;
      // Unauthorized notifications clear these caches synchronously, before React
      // necessarily commits the workspace unmount. Do not repopulate that scope.
      const session = cache.getQueryData<Session | null>(['session']);
      const project = cache.getQueryData<Project | null>([
        'project',
        value.authorization.project.id,
      ]);
      return (
        session?.user_id === value.authorization.session.user_id &&
        session.csrf_token === value.authorization.session.csrf_token &&
        !!project &&
        [...project.permissions].sort().join(',') ===
          [...value.authorization.project.permissions].sort().join(',')
      );
    },
    [cache],
  );
  const invalidate = useCallback(() => {
    current.current.generation += 1;
  }, []);
  return { capture, isCurrent, invalidate };
}
