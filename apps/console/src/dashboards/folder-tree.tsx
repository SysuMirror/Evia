import { useId, useMemo, useState } from 'react';
import type { DashboardFolder } from '@evia/api-client';
import { Button, DataSurface, Field, Input, Modal, Select } from '@evia/design-system';
import { CatalogError } from '../catalog-helpers';
import { eligibleParents } from './use-dashboard-folders';
import type { DashboardFolderScope } from './types';

type FolderTreeProps = {
  items: DashboardFolder[];
  loading: boolean;
  fetching: boolean;
  error: Error | null;
  canWrite: boolean;
  busy: boolean;
  scope: DashboardFolderScope;
  onScopeChange: (scope: DashboardFolderScope) => void;
  onCreate: (input: { title: string; parentID: string }, done: () => void) => void;
  onRename: (folder: DashboardFolder, title: string, done: () => void) => void;
  onDelete: (folder: DashboardFolder, done: () => void) => void;
  onRetry: () => void;
};

type ModalState =
  | { mode: 'create'; parentID: string }
  | { mode: 'rename'; folder: DashboardFolder }
  | { mode: 'delete'; folder: DashboardFolder }
  | null;

export function DashboardFolderTree({
  items,
  loading,
  fetching,
  error,
  canWrite,
  busy,
  scope,
  onScopeChange,
  onCreate,
  onRename,
  onDelete,
  onRetry,
}: FolderTreeProps) {
  const id = useId();
  const [modal, setModal] = useState<ModalState>(null);
  const [title, setTitle] = useState('');
  const [parentID, setParentID] = useState('');
  const sorted = useMemo(
    () => [...items].sort((a, b) => a.title.localeCompare(b.title, 'zh-Hans-CN')),
    [items],
  );
  const parents = useMemo(() => eligibleParents(sorted), [sorted]);

  function openCreate(initialParent = '') {
    setTitle('');
    setParentID(initialParent);
    setModal({ mode: 'create', parentID: initialParent });
  }
  function openRename(folder: DashboardFolder) {
    setTitle(folder.title);
    setModal({ mode: 'rename', folder });
  }
  function closeModal() {
    if (!busy) setModal(null);
  }
  function submit() {
    const trimmed = title.trim();
    if (!trimmed || !modal) return;
    if (modal.mode === 'create') {
      onCreate({ title: trimmed, parentID }, () => setModal(null));
    } else if (modal.mode === 'rename') {
      onRename(modal.folder, trimmed, () => setModal(null));
    }
  }
  const scopeButton = (active: boolean, label: string, onClick: () => void, key: string) => (
    <button
      key={key}
      type="button"
      className="dashboard-folder-node"
      aria-pressed={active}
      onClick={onClick}
    >
      <span className={`dashboard-folder-label${active ? ' is-active' : ''}`}>{label}</span>
    </button>
  );
  return (
    <DataSurface label="文件夹">
      <div className="dashboard-folder-tree" aria-busy={fetching || loading}>
        {scopeButton(scope === '', '全部看板', () => onScopeChange(''), 'all')}
        {scopeButton(scope === 'root', '未归档（根级）', () => onScopeChange('root'), 'root')}
        {sorted.map((folder) => {
          const active = scope === folder.folder_id;
          return (
            <div
              key={folder.folder_id}
              className="dashboard-folder-node"
              style={{ paddingLeft: `${(folder.depth - 1) * 16 + 8}px` }}
            >
              <button
                type="button"
                className={`dashboard-folder-label${active ? ' is-active' : ''}`}
                aria-pressed={active}
                onClick={() => onScopeChange(folder.folder_id)}
              >
                {folder.title}
              </button>
              {canWrite && (
                <span className="dashboard-folder-actions">
                  {folder.depth < 3 && (
                    <Button
                      size="small"
                      variant="ghost"
                      disabled={busy}
                      onClick={() => openCreate(folder.folder_id)}
                    >
                      子文件夹
                    </Button>
                  )}
                  <Button
                    size="small"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => openRename(folder)}
                  >
                    改名
                  </Button>
                  <Button
                    size="small"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => setModal({ mode: 'delete', folder })}
                  >
                    删除
                  </Button>
                </span>
              )}
            </div>
          );
        })}
        {!loading && items.length === 0 && (
          <p className="field-help">暂无文件夹；可新建文件夹为看板分组。</p>
        )}
      </div>
      <CatalogError error={error} retry={onRetry} />
      {canWrite && (
        <div className="dashboard-folder-toolbar">
          <Button size="small" disabled={busy} onClick={() => openCreate('')}>
            新建根文件夹
          </Button>
        </div>
      )}
      <Modal
        open={modal?.mode === 'create'}
        title={modal && modal.mode === 'create' && modal.parentID ? '新建子文件夹' : '新建文件夹'}
        busy={busy}
        onRequestClose={closeModal}
        footer={
          <>
            <Button disabled={busy} onClick={closeModal}>
              取消
            </Button>
            <Button variant="primary" disabled={busy || !title.trim()} onClick={submit}>
              确认新建
            </Button>
          </>
        }
      >
        <form
          className="section-stack"
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          <Field label="文件夹标题" htmlFor={`${id}-title`} required>
            <Input
              id={`${id}-title`}
              value={title}
              maxLength={128}
              required
              autoFocus
              onChange={(event) => setTitle(event.target.value)}
            />
          </Field>
          <Field
            label="父文件夹"
            htmlFor={`${id}-parent`}
            hint="留空表示根级文件夹；层级最深 3 层。"
          >
            <Select
              id={`${id}-parent`}
              value={parentID}
              onChange={(event) => setParentID(event.target.value)}
              options={[
                { value: '', label: '根级（无父文件夹）' },
                ...parents.map((folder) => ({
                  value: folder.folder_id,
                  label: `${'　'.repeat(folder.depth - 1)}${folder.title}`,
                })),
              ]}
            />
          </Field>
        </form>
      </Modal>
      <Modal
        open={modal?.mode === 'rename'}
        title="重命名文件夹"
        busy={busy}
        onRequestClose={closeModal}
        footer={
          <>
            <Button disabled={busy} onClick={closeModal}>
              取消
            </Button>
            <Button variant="primary" disabled={busy || !title.trim()} onClick={submit}>
              保存改名
            </Button>
          </>
        }
      >
        <Field label="文件夹标题" htmlFor={`${id}-rename`} required>
          <Input
            id={`${id}-rename`}
            value={title}
            maxLength={128}
            required
            autoFocus
            onChange={(event) => setTitle(event.target.value)}
          />
        </Field>
      </Modal>
      <Modal
        open={modal?.mode === 'delete'}
        title="确认删除文件夹"
        busy={busy}
        onRequestClose={closeModal}
        footer={
          <>
            <Button disabled={busy} onClick={closeModal}>
              取消
            </Button>
            <Button
              variant="danger"
              disabled={busy}
              onClick={() =>
                modal?.mode === 'delete' && onDelete(modal.folder, () => setModal(null))
              }
            >
              确认删除
            </Button>
          </>
        }
      >
        <p>
          仅空文件夹可以删除。文件夹「
          <strong>{modal?.mode === 'delete' ? modal.folder.title : ''}</strong>
          」仍包含子文件夹或看板时，服务端会拒绝删除；请先移动其中内容。
        </p>
      </Modal>
    </DataSurface>
  );
}
