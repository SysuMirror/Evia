import { ApiError } from '@evia/api-client';
import { ErrorPanel } from '@evia/design-system';
export function MachineFailure({ error, retry }: { error: Error; retry?: () => void }) {
  return (
    <ErrorPanel
      message={error.message}
      requestID={error instanceof ApiError ? error.requestID : undefined}
      retry={retry}
    />
  );
}
