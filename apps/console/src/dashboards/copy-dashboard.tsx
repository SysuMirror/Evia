import { useId, useMemo, useState } from 'react';
import type { Dashboard, DashboardFolder } from '@evia/api-client';
import { Button, Field, Input, Modal, Select } from '@evia/design-system';

type CopyDashboardModalProps = {
  dashboard: Dashboard | null;
  folders: DashboardFolder[];
  busy: boolean;
  onClose: () => void;
  onCopy: (dashboard: Dashboard, title: string, folderID: string) => void;
};

/**
 * Same-environment copy: the new dashboard starts as an unpublished draft with
 * a fresh ID; the target folder defaults to the source dashboard's folder.
 */
export function CopyDashboardModal({
  dashboard,
  folders,
  busy,
  onClose,
  onCopy,
}: CopyDashboardModalProps) {
  const id = useId();
  const sorted = useMemo(
    () => [...folders].sort((a, b) => a.title.localeCompare(b.title, 'zh-Hans-CN')),
    [folders],
  );
  const [title, setTitle] = useState('');
  const [folderID, setFolderID] = useState('');
  // The modal remounts per source dashboard (keyed by ID at the call site), so
  // lazy initial state picks up each source's title and folder exactly once.
  const initialTitle = title || (dashboard ? `${dashboard.title} 副本` : '');
  const targetFolder = folderID || (dashboard?.folder_id ?? '');
  const valid = initialTitle.trim().length > 0 && initialTitle.trim().length <= 128;
  return (
    <Modal
      open={!!dashboard}
      title="复制看板"
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
            disabled={busy || !valid}
            onClick={() => dashboard && onCopy(dashboard, initialTitle.trim(), targetFolder)}
          >
            确认复制
          </Button>
        </>
      }
    >
      <div className="section-stack">
        <p className="field-help">
          副本在同一项目和环境内创建，生成全新的看板与组件标识，保留固定引用、布局、参数与绑定；发布状态、运行记录与归档状态不会被复制。
        </p>
        <Field label="新看板标题" htmlFor={`${id}-copy-title`} hint="1 至 128 个字符。">
          <Input
            id={`${id}-copy-title`}
            value={initialTitle}
            maxLength={128}
            required
            onChange={(event) => setTitle(event.target.value)}
          />
        </Field>
        <Field label="目标文件夹" htmlFor={`${id}-copy-folder`}>
          <Select
            id={`${id}-copy-folder`}
            value={targetFolder}
            onChange={(event) => setFolderID(event.target.value)}
            options={[
              { value: '', label: '根级（不属于任何文件夹）' },
              ...sorted.map((folder) => ({
                value: folder.folder_id,
                label: `${'　'.repeat(folder.depth - 1)}${folder.title}`,
              })),
            ]}
          />
        </Field>
      </div>
    </Modal>
  );
}
