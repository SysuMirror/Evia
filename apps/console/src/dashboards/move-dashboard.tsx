import { useId, useMemo, useState } from 'react';
import type { Dashboard, DashboardFolder } from '@evia/api-client';
import { Button, Field, Modal, Select } from '@evia/design-system';

type MoveDashboardProps = {
  dashboard: Dashboard | null;
  folders: DashboardFolder[];
  busy: boolean;
  onClose: () => void;
  onMove: (dashboard: Dashboard, folderID: string) => void;
};

/**
 * Moving reuses the draft PATCH: the selected folder is a full-replacement
 * field, and choosing the root option sends an empty folder_id.
 */
export function MoveDashboardModal({
  dashboard,
  folders,
  busy,
  onClose,
  onMove,
}: MoveDashboardProps) {
  const id = useId();
  const sorted = useMemo(
    () => [...folders].sort((a, b) => a.title.localeCompare(b.title, 'zh-Hans-CN')),
    [folders],
  );
  const [target, setTarget] = useState(dashboard?.folder_id ?? '');
  const unchanged = dashboard ? target === (dashboard.folder_id ?? '') : true;
  return (
    <Modal
      open={!!dashboard}
      title="移动看板到文件夹"
      busy={busy}
      onRequestClose={() => {
        if (!busy) onClose();
      }}
      footer={
        <>
          <Button disabled={busy} onClick={onClose}>
            取消
          </Button>
          <Button
            variant="primary"
            disabled={busy || unchanged}
            onClick={() => dashboard && onMove(dashboard, target)}
          >
            确认移动
          </Button>
        </>
      }
    >
      <Field label="目标文件夹" htmlFor={`${id}-target`} hint="选择根级可将看板移出所有文件夹。">
        <Select
          id={`${id}-target`}
          value={target}
          onChange={(event) => setTarget(event.target.value)}
          options={[
            { value: '', label: '根级（不属于任何文件夹）' },
            ...sorted.map((folder) => ({
              value: folder.folder_id,
              label: `${'　'.repeat(folder.depth - 1)}${folder.title}`,
            })),
          ]}
        />
      </Field>
    </Modal>
  );
}
