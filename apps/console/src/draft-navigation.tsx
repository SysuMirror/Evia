import { createContext, useCallback, useContext, useRef, type ReactNode } from 'react';

type DraftGuard = (proceed: () => void) => void;
const DraftNavigation = createContext<{
  register: (guard: DraftGuard) => () => void;
  request: (proceed: () => void) => void;
} | null>(null);

export function DraftNavigationProvider({ children }: { children: ReactNode }) {
  const guard = useRef<DraftGuard | null>(null);
  const register = useCallback((next: DraftGuard) => {
    guard.current = next;
    return () => {
      if (guard.current === next) guard.current = null;
    };
  }, []);
  const request = useCallback((proceed: () => void) => {
    if (guard.current) guard.current(proceed);
    else proceed();
  }, []);
  return (
    <DraftNavigation.Provider value={{ register, request }}>{children}</DraftNavigation.Provider>
  );
}

export function useDraftNavigation() {
  const navigation = useContext(DraftNavigation);
  if (!navigation) throw new Error('DraftNavigationProvider is required');
  return navigation;
}
