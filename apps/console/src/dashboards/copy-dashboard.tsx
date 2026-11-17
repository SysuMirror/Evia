import { useId, useMemo, useState } from 'react';
import type { CatalogPageProps } from '../catalog';
import type { Dashboard, DashboardFolder, Environment } from '@evia/api-client';
import { Button, Field, Input, Modal, Select } from '@evia/design-system';
import { CrossEnvironmentCopyForm } from './cross-env-copy';
import type { DashboardCopyRequest } from './types';

type CopyDashboardModalProps = {
  dashboard: Dashboard | null;
  folders: DashboardFolder[];
  sourceEnvironment: Environment;
  context: CatalogPageProps;
  busy: boolean;
  onClose: () => void;
  onSubmit: (dashboard: Dashboard, request: DashboardCopyRequest) => void;
};

/**
 * Copy modal. Same-environment mode reproduces the current draft with fresh
 * IDs and retained references; cross-environment mode requires an explicit
 * target and exhaustive per-widget rebinding handled by its own form.
 */
export function CopyDashboardModal({
  dashboard,
  folders,
  sourceEnvironment,
  context,
  busy,
  onClose,
  onSubmit,
}: CopyDashboardModalProps) {
  const id = useId();
  const sorted = useMemo(
    () => [...folders].sort((a, b) => a.title.localeCompare(b.title, 'zh-Hans-CN')),
    [folders],
  );
  const [mode, setMode] = useState<'same' | 'cross'>('same');
  const [title, setTitle] = useState('');
  const [folderID, setFolderID] = useState('');
  const [crossRequest, setCrossRequest] = useState<DashboardCopyRequest | null>(null);
  // The modal remounts per source dashboard (keyed by ID at the call site), so
  // lazy initial state picks up each source's title and folder exactly once.
  const initialTitle = title || (dashboard ? `${dashboard.title} 副本` : '');
  const targetFolder = folderID || (dashboard?.folder_id ?? '');
  const sameValid = initialTitle.trim().length > 0 && initialTitle.trim().length <= 128;

  function confirm() {
    if (!dashboard) return;
    if (mode === 'cross') {
      if (crossRequest) onSubmit(dashboard, crossRequest);
      return;
    }
    if (sameValid) {
      onSubmit(dashboard, { title: initialTitle.trim(), folderID: targetFolder });
    }
  }

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
            disabled={busy || (mode === 'cross' ? !crossRequest : !sameValid)}
            onClick={confirm}
          >
            确认复制
          </Button>
        </>
      }
    >
      <div className="section-stack">
        <Field label="复制方式" htmlFor={`${id}-copy-mode`}>
          <Select
            value={mode}
            onChange={(event) => setMode(event.target.value as 'same' | 'cross')}
            options={[
              { value: 'same', label: `同环境复制（${sourceEnvironment}）` },
              { value: 'cross', label: '跨环境复制（需逐组件重绑定）' },
            ]}
          />
        </Field>
        {mode === 'same' ? (
          <>
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
          </>
        ) : (
          dashboard && (
            <CrossEnvironmentCopyForm
              dashboard={dashboard}
              sourceEnvironment={sourceEnvironment}
              context={context}
              onChange={setCrossRequest}
            />
          )
        )}
      </div>
    </Modal>
  );
}
