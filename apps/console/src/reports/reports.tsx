import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type Project,
  type Report,
  type Session,
} from '@evia/api-client';
import {
  Alert,
  Button,
  DataSurface,
  DataTable,
  EmptyPanel,
  Field,
  Input,
  LoadingPanel,
  Modal,
  PageHeading,
  PaginationBar,
  Select,
  Textarea,
  Toolbar,
  useToast,
  type DataTableColumn,
} from '@evia/design-system';
import { CatalogError } from '../catalog-helpers';
import {
  addSection,
  emptyReportForm,
  formatInstant,
  normalizeReportSearch,
  patchSection,
  removeSection,
  reportFormFrom,
  REPORT_DESCRIPTION_MAX,
  REPORT_MAX_SECTIONS,
  REPORT_SECTION_TITLE_MAX,
  REPORT_TITLE_MAX,
  toReportInput,
  versionLabel,
  type ReportFormState,
} from './model';
import { useReportsRequest } from './use-reports-request';
import './styles.css';

type PageProps = { project: Project; session: Session; environment: Environment };
const PAGE_LIMIT = 20;

/** Debounce window for the title search box so typing never fires a request per keystroke. */
const searchDebounceMs = 300;

function shortID(id: string): string {
  return id.length > 13 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id;
}

/** Fetches the immutable versions of one analysis only while it is selected. */
function VersionSelect({
  project,
  analysisID,
  value,
  disabled,
  onChange,
  onDenied,
}: {
  project: Project;
  analysisID: string;
  value: number;
  disabled: boolean;
  onChange: (version: number) => void;
  onDenied: (error: Error) => void;
}) {
  const versions = useQuery({
    queryKey: ['reports', project.id, 'versions', analysisID],
    queryFn: ({ signal }) =>
      api.listSavedAnalysisVersions(project.id, analysisID, { limit: 50 }, signal),
    enabled: !!analysisID,
  });
  useEffect(() => {
    if (
      versions.error &&
      'status' in versions.error &&
      [401, 403].includes(Number(versions.error.status))
    ) {
      onDenied(versions.error);
    }
  }, [versions.error, onDenied]);
  return (
    <Select
      value={String(value)}
      disabled={disabled || !analysisID || versions.isFetching}
      options={[
        { value: '0', label: analysisID ? '选择不可变版本' : '请先选择分析' },
        ...(versions.data?.items ?? []).map((item) => ({
          value: String(item.version),
          label: versionLabel(item),
        })),
      ]}
      onChange={(event) => onChange(Number(event.target.value))}
    />
  );
}

type EditorMode = { kind: 'create' } | { kind: 'edit'; report: Report };

function ReportEditorModal({
  open,
  mode,
  project,
  session,
  environment,
  busy,
  onClose,
  onDenied,
  onSaved,
}: {
  open: boolean;
  mode: EditorMode;
  project: Project;
  session: Session;
  environment: Environment;
  busy: boolean;
  onClose: () => void;
  onDenied: (error: Error) => void;
  onSaved: (operation: string, request: (signal: AbortSignal) => Promise<Report>) => void;
}) {
  const [form, setForm] = useState<ReportFormState>(emptyReportForm());
  const [errors, setErrors] = useState<string[]>([]);
  const editing = mode.kind === 'edit' ? mode.report : null;

  useEffect(() => {
    if (open) {
      setForm(editing ? reportFormFrom(editing) : emptyReportForm());
      setErrors([]);
    }
  }, [open, editing]);

  const analyses = useQuery({
    queryKey: ['reports', project.id, environment, 'analyses'],
    queryFn: ({ signal }) => api.listSavedAnalyses(project.id, { environment, limit: 100 }, signal),
    enabled: open,
  });
  useEffect(() => {
    if (
      analyses.error &&
      'status' in analyses.error &&
      [401, 403].includes(Number(analyses.error.status))
    ) {
      onDenied(analyses.error);
    }
  }, [analyses.error, onDenied]);

  const published = useMemo(
    () => (analyses.data?.items ?? []).filter((item) => item.published_version > 0),
    [analyses.data],
  );

  const submit = () => {
    const built = toReportInput(environment, form);
    if (!built.input) {
      setErrors(built.errors);
      return;
    }
    const key = crypto.randomUUID();
    const operation = editing ? 'report.update' : 'report.create';
    const request = (signal: AbortSignal) =>
      editing
        ? api.updateReport(
            project.id,
            editing.report_id,
            built.input!,
            editing.revision,
            key,
            session.csrf_token,
            signal,
          )
        : api.createReport(project.id, built.input!, key, session.csrf_token, signal);
    onSaved(operation, request);
  };

  return (
    <Modal
      open={open}
      title={editing ? `编辑报告 ${shortID(editing.report_id)}` : '新建分析报告'}
      onRequestClose={onClose}
      busy={busy}
      size="wide"
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button variant="primary" onClick={submit} loading={busy}>
            {editing ? '保存修订' : '创建报告'}
          </Button>
        </>
      }
    >
      {errors.length > 0 && (
        <div className="report-form-errors">
          <Alert tone="error" title="请先修正以下问题" />
          <ul>
            {errors.map((message) => (
              <li key={message}>{message}</li>
            ))}
          </ul>
        </div>
      )}
      <div className="report-form-grid">
        <Field label="报告标题" htmlFor="report-title">
          <Input
            id="report-title"
            value={form.title}
            maxLength={REPORT_TITLE_MAX}
            placeholder="例如：11 月学习行为周报"
            autoComplete="off"
            disabled={busy}
            onChange={(event) => setForm((current) => ({ ...current, title: event.target.value }))}
          />
        </Field>
        <Field label="报告描述" htmlFor="report-description">
          <Textarea
            id="report-description"
            value={form.description}
            maxLength={REPORT_DESCRIPTION_MAX}
            rows={2}
            placeholder="可选，说明本报告的阅读目标"
            disabled={busy}
            onChange={(event) =>
              setForm((current) => ({ ...current, description: event.target.value }))
            }
          />
        </Field>
      </div>
      <CatalogError error={analyses.error} retry={() => void analyses.refetch()} />
      {analyses.isPending ? (
        <LoadingPanel label="正在读取已发布分析" />
      ) : published.length === 0 ? (
        <p className="evia-field-hint">
          当前环境还没有已发布的保存分析。请先在「保存的分析」中创建并发布，再回到这里编排报告。
        </p>
      ) : (
        <div className="report-sections">
          <div className="report-sections-head">
            <strong>章节（按阅读顺序固定引用不可变版本）</strong>
            <Button
              variant="secondary"
              disabled={busy || form.sections.length >= REPORT_MAX_SECTIONS}
              onClick={() => setForm((current) => addSection(current))}
            >
              添加章节
            </Button>
          </div>
          {form.sections.map((section, index) => (
            <div className="report-section-row" key={index}>
              <span className="report-order">第 {index + 1} 章</span>
              <Input
                value={section.sectionTitle}
                maxLength={REPORT_SECTION_TITLE_MAX}
                placeholder="章节标题"
                autoComplete="off"
                disabled={busy}
                onChange={(event) =>
                  setForm((current) =>
                    patchSection(current, index, { sectionTitle: event.target.value }),
                  )
                }
              />
              <Select
                value={section.analysisID}
                disabled={busy}
                options={[
                  { value: '', label: '选择已发布分析' },
                  ...published.map((item) => ({
                    value: item.analysis_id,
                    label: `${item.title}（最新 v${item.published_version}）`,
                  })),
                ]}
                onChange={(event) =>
                  setForm((current) =>
                    patchSection(current, index, {
                      analysisID: event.target.value,
                      analysisVersion: 0,
                    }),
                  )
                }
              />
              <VersionSelect
                project={project}
                analysisID={section.analysisID}
                value={section.analysisVersion}
                disabled={busy}
                onDenied={onDenied}
                onChange={(version) =>
                  setForm((current) => patchSection(current, index, { analysisVersion: version }))
                }
              />
              <Button
                variant="ghost"
                disabled={busy || form.sections.length === 1}
                onClick={() => setForm((current) => removeSection(current, index))}
              >
                移除
              </Button>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}

export function ReportsPage({ project, session, environment }: PageProps) {
  const toast = useToast();
  const canWrite = project.permissions.includes('project.write');
  const requests = useReportsRequest(
    project,
    session,
    `${project.id}:${session.user_id}:${session.csrf_token}`,
    true,
  );
  const [cursors, setCursors] = useState<string[]>([]);
  const [editor, setEditor] = useState<EditorMode | null>(null);
  const [deleting, setDeleting] = useState<Report | null>(null);
  const [notice, setNotice] = useState('');
  // searchInput mirrors the textbox immediately; search is the debounced,
  // normalized term actually bound to the request and to any page cursor.
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');

  useEffect(() => {
    const timer = setTimeout(() => {
      const next = normalizeReportSearch(searchInput);
      setSearch((previous) => (next === previous ? previous : next));
    }, searchDebounceMs);
    return () => clearTimeout(timer);
  }, [searchInput]);
  // A new predicate invalidates any cursor collected under the previous term.
  useEffect(() => {
    setCursors([]);
  }, [search]);

  const reportsQuery = useQuery({
    queryKey: ['reports', project.id, environment, search, cursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listReports(
        project.id,
        environment,
        { cursor: cursors.at(-1), limit: PAGE_LIMIT, search: search || undefined },
        signal,
      ),
  });

  const refresh = () => {
    setNotice('');
    void reportsQuery.refetch();
  };

  if (requests.denied) {
    return (
      <>
        <PageHeading title="分析报告" />
        <Alert tone="error" title={requests.denied.message} />
      </>
    );
  }

  const save = (operation: string, request: (signal: AbortSignal) => Promise<Report>) => {
    void requests.perform(
      operation,
      request,
      (report) => {
        setEditor(null);
        refresh();
        toast.success(
          operation === 'report.create'
            ? `报告 ${shortID(report.report_id)} 已创建，当前修订 v${report.revision}。`
            : `报告 ${shortID(report.report_id)} 已保存，当前修订 v${report.revision}。`,
        );
      },
      (error) => {
        if (error instanceof ApiError && error.status === 409) {
          setNotice('修订或幂等冲突：资源可能已被他人修改，请重新加载后再编辑。');
        } else {
          setNotice(error.message);
        }
      },
    );
  };

  const confirmDelete = () => {
    if (!deleting) return;
    const target = deleting;
    void requests.perform(
      'report.delete',
      (signal) => api.deleteReport(project.id, target.report_id, session.csrf_token, signal),
      () => {
        setDeleting(null);
        refresh();
        toast.success(`报告 ${shortID(target.report_id)} 已删除。`);
      },
      (error) => {
        setDeleting(null);
        setNotice(error.message);
      },
    );
  };

  const columns: DataTableColumn<Report>[] = [
    {
      key: 'title',
      header: '报告',
      render: (row) => (
        <div>
          <strong>{row.definition.title}</strong>
          {row.definition.description && (
            <div className="evia-field-hint report-cell-description">
              {row.definition.description}
            </div>
          )}
        </div>
      ),
    },
    {
      key: 'sections',
      header: '章节数',
      render: (row) => String(row.definition.sections.length),
    },
    {
      key: 'revision',
      header: '修订',
      render: (row) => `v${row.revision}`,
    },
    {
      key: 'updated_at',
      header: '更新时间',
      render: (row) => formatInstant(row.updated_at),
    },
    {
      key: 'actions',
      header: '操作',
      kind: 'actions',
      render: (row) =>
        canWrite ? (
          <div className="report-actions">
            <Button
              variant="secondary"
              disabled={!!requests.busy}
              onClick={() => setEditor({ kind: 'edit', report: row })}
            >
              编辑
            </Button>
            <Button variant="danger" disabled={!!requests.busy} onClick={() => setDeleting(row)}>
              删除
            </Button>
          </div>
        ) : (
          <span className="evia-field-hint">只读</span>
        ),
    },
  ];

  return (
    <>
      <PageHeading title="分析报告" />
      <div className="report-intro inline-notice">
        <Alert
          tone="info"
          title="报告是按阅读顺序编排的文档：每个章节都通过外键固定一个已发布的保存分析不可变版本，草稿或已删除的分析版本无法被引用。仅项目所有者与编辑者可写，成员保持只读。"
        />
      </div>
      {notice && <Alert tone="error" title={notice} />}
      <DataSurface label={`${environment} 环境报告`}>
        <Toolbar
          label="报告列表"
          actions={
            <Input
              className="report-search-input"
              type="search"
              aria-label="搜索报告标题"
              placeholder="按标题搜索…"
              maxLength={128}
              value={searchInput}
              onChange={(event) => setSearchInput(event.target.value)}
            />
          }
        >
          {canWrite && (
            <Button
              variant="primary"
              disabled={!!requests.busy}
              onClick={() => setEditor({ kind: 'create' })}
            >
              新建报告
            </Button>
          )}
        </Toolbar>
        <CatalogError error={reportsQuery.error} retry={() => void reportsQuery.refetch()} />
        {reportsQuery.isPending ? (
          <LoadingPanel label="正在读取报告" />
        ) : (reportsQuery.data?.items.length ?? 0) === 0 ? (
          <EmptyPanel description="当前环境还没有报告。" />
        ) : (
          <DataTable
            caption="分析报告列表"
            rows={reportsQuery.data?.items ?? []}
            columns={columns}
            rowKey={(row) => row.report_id}
          />
        )}
        {(cursors.length > 0 || reportsQuery.data?.next_cursor) && (
          <PaginationBar
            page={cursors.length + 1}
            count={reportsQuery.data?.items.length ?? 0}
            hasNext={!!reportsQuery.data?.next_cursor}
            busy={reportsQuery.isFetching}
            onPrevious={() => setCursors((current) => current.slice(0, -1))}
            onNext={() => {
              if (reportsQuery.data?.next_cursor) {
                setCursors((current) => [...current, reportsQuery.data!.next_cursor!]);
              }
            }}
          />
        )}
      </DataSurface>
      {editor && (
        <ReportEditorModal
          open
          mode={editor}
          project={project}
          session={session}
          environment={environment}
          busy={!!requests.busy}
          onClose={() => setEditor(null)}
          onDenied={requests.deny}
          onSaved={save}
        />
      )}
      <Modal
        open={!!deleting}
        title="删除报告"
        onRequestClose={() => setDeleting(null)}
        busy={requests.busy === 'report.delete'}
        footer={
          <>
            <Button onClick={() => setDeleting(null)} disabled={!!requests.busy}>
              取消
            </Button>
            <Button
              variant="danger"
              loading={requests.busy === 'report.delete'}
              onClick={confirmDelete}
            >
              确认删除
            </Button>
          </>
        }
      >
        <p>
          确认删除报告「{deleting?.definition.title}
          」？其章节将随报告级联删除，该操作会写入审计日志且不可撤销。
        </p>
      </Modal>
    </>
  );
}
