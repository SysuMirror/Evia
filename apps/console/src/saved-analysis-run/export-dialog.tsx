import { useEffect, useId, useRef, useState } from 'react';
import {
  api,
  ApiError,
  type ExportJob,
  type Project,
  type SavedAnalysisVersion,
  type Session,
} from '@evia/api-client';
import { Button, Field, Input, Modal, Select, useToast } from '@evia/design-system';
import {
  buildSavedExportInput,
  defaultSavedExportForm,
  savedExportFormatOptions,
  type SavedExportFormState,
} from './export-model';
import type { SavedRunRange } from './types';

/**
 * Binds the current run-view time window to an immutable saved analysis
 * version and enqueues it as an export job. The server owns snapshot
 * resolution, pin verification and the archived fail-closed guard.
 */
export function SavedExportDialog({
  open,
  onClose,
  version,
  range,
  project,
  session,
  onAccessDenied,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  version: SavedAnalysisVersion;
  range: SavedRunRange;
  project: Project;
  session: Session;
  onAccessDenied: (error: Error) => void;
  onCreated: (job: ExportJob) => void;
}) {
  const id = useId();
  const toast = useToast();
  const [form, setForm] = useState<SavedExportFormState>(() => defaultSavedExportForm(version));
  const [errors, setErrors] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const request = useRef<AbortController | null>(null);

  useEffect(() => {
    if (open) {
      setForm(defaultSavedExportForm(version));
      setErrors([]);
    }
  }, [open, version]);

  useEffect(() => () => request.current?.abort(), []);

  const submit = async () => {
    if (busy) return;
    const built = buildSavedExportInput(range, version.definition.query.kind, form);
    if (!built.input) {
      setErrors(built.errors);
      return;
    }
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setErrors([]);
    try {
      const job = await api.createSavedAnalysisExportJob(
        project.id,
        version.analysis_id,
        version.version,
        built.input,
        crypto.randomUUID(),
        session.csrf_token,
        controller.signal,
      );
      toast.success(`导出作业已入队，可在数据导出工作区查看进度与下载授权。`);
      onCreated(job);
      onClose();
    } catch (failure) {
      if (controller.signal.aborted) return;
      const error = failure instanceof Error ? failure : new Error('请求失败，请重试。');
      if (failure instanceof ApiError && [401, 403].includes(failure.status)) {
        onAccessDenied(error);
        onClose();
        return;
      }
      setErrors([
        failure instanceof ApiError && failure.status === 409
          ? '幂等键冲突或保存分析已归档，请刷新后重试。'
          : error.message,
      ]);
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      title={`导出 v${version.version}`}
      onRequestClose={() => {
        if (!busy) onClose();
      }}
      footer={
        <>
          <Button onClick={() => !busy && onClose()} disabled={busy}>
            取消
          </Button>
          <Button variant="primary" loading={busy} onClick={() => void submit()}>
            创建导出作业
          </Button>
        </>
      }
    >
      <form
        className="section-stack"
        aria-describedby={errors.length > 0 ? `${id}-errors` : undefined}
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        {errors.length > 0 && (
          <div id={`${id}-errors`} role="alert" className="inline-notice">
            {errors.map((message) => (
              <p key={message}>{message}</p>
            ))}
          </div>
        )}
        <p className="field-help">
          服务端按已发布的不可变版本绑定查询，时间范围沿用当前执行设置：
          {range.from.replace('T', ' ')} 至 {range.to.replace('T', ' ')}（不含），显示时区{' '}
          {range.timezone}。
        </p>
        <Field label="导出名称" htmlFor={`${id}-name`}>
          <Input
            id={`${id}-name`}
            value={form.name}
            maxLength={200}
            required
            onChange={(event) => {
              setForm((current) => ({ ...current, name: event.target.value }));
              setErrors([]);
            }}
          />
        </Field>
        <Field label="产物格式" htmlFor={`${id}-format`}>
          <Select
            id={`${id}-format`}
            value={form.format}
            options={savedExportFormatOptions}
            onChange={(event) =>
              setForm((current) => ({
                ...current,
                format: event.target.value as SavedExportFormState['format'],
              }))
            }
          />
        </Field>
      </form>
    </Modal>
  );
}
