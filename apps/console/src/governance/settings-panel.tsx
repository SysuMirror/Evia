import { useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, ApiError, type Project, type ProjectSettings, type Session } from '@evia/api-client';
import { Alert, Button, Field, Input, Panel, useToast } from '@evia/design-system';
import { isRevisionConflict, isUncertainWrite } from './model';
import type { GovernancePerform } from './use-governance-request';

interface SettingsPanelProps {
  project: Project;
  session: Session;
  canEdit: boolean;
  perform: GovernancePerform;
  busy: string;
  revisionConflict: () => void;
}

interface PendingSettingsIntent {
  revision: number;
  name: string;
  timezone: string;
  key: string;
}

export function SettingsPanel({
  project,
  session,
  canEdit,
  perform,
  busy,
  revisionConflict,
}: SettingsPanelProps) {
  const cache = useQueryClient();
  const toast = useToast();
  const queryKey = ['governance', project.id, session.user_id, 'settings'] as const;
  const settings = useQuery({
    queryKey,
    queryFn: ({ signal }) => api.getSettings(project.id, signal),
  });
  const [name, setName] = useState('');
  const [timezone, setTimezone] = useState('');
  const [formError, setFormError] = useState('');
  const [writeError, setWriteError] = useState('');
  const [pending, setPending] = useState<PendingSettingsIntent>();
  const [loadedRevision, setLoadedRevision] = useState(0);

  // Adopt server values whenever a fresh revision arrives; never overwrite a
  // retained unknown-result intent or the user's in-progress edit.
  useEffect(() => {
    if (!settings.data || settings.data.revision === loadedRevision) return;
    setName(settings.data.name);
    setTimezone(settings.data.timezone);
    setLoadedRevision(settings.data.revision);
  }, [settings.data, loadedRevision]);

  if (settings.isPending) return <Panel title="项目设置">正在加载设置…</Panel>;
  if (settings.isError || !settings.data)
    return (
      <Alert
        tone="error"
        title="无法读取项目设置"
        action={<Button onClick={() => void settings.refetch()}>重试</Button>}
      >
        {settings.error instanceof ApiError ? settings.error.message : '请稍后再试。'}
      </Alert>
    );

  const current = settings.data;
  const saving = busy === 'settings.update';
  const changed = name.trim() !== current.name || timezone.trim() !== current.timezone;

  const persist = (intent: PendingSettingsIntent) => {
    setWriteError('');
    void perform(
      'settings.update',
      (signal) =>
        api.updateSettings(
          project.id,
          intent.revision,
          { name: intent.name, timezone: intent.timezone },
          intent.key,
          session.csrf_token,
          signal,
        ),
      (updated: ProjectSettings) => {
        setPending(undefined);
        cache.setQueryData(queryKey, updated);
        setName(updated.name);
        setTimezone(updated.timezone);
        setLoadedRevision(updated.revision);
        toast.success('项目设置已更新');
      },
      (error) => {
        if (isRevisionConflict(error)) {
          setPending(undefined);
          setWriteError('设置已被其他人修改，已为你重新加载，请核对后再提交。');
          revisionConflict();
        } else if (isUncertainWrite(error)) {
          // Keep the original body, revision and key; the commit may have landed.
          setPending(intent);
          setWriteError('提交结果未知，已保留原请求，请用同一幂等键重试确认。');
        } else {
          setPending(undefined);
          setWriteError(error.message);
        }
      },
    );
  };

  const submit = () => {
    const nextName = name.trim();
    const nextTimezone = timezone.trim();
    if (nextName.length < 1 || nextName.length > 100) {
      setFormError('项目名称需为 1–100 个字符。');
      return;
    }
    if (nextTimezone.length < 1 || nextTimezone.toLowerCase() === 'local') {
      setFormError('请填写有效的 IANA 时区，例如 Asia/Shanghai，不能使用 Local。');
      return;
    }
    setFormError('');
    const intent: PendingSettingsIntent = pending
      ? { ...pending, name: nextName, timezone: nextTimezone }
      : {
          revision: current.revision,
          name: nextName,
          timezone: nextTimezone,
          key: crypto.randomUUID(),
        };
    persist(intent);
  };

  return (
    <Panel
      title="项目设置"
      action={
        canEdit ? undefined : (
          <Alert tone="info" title="当前角色仅可查看设置，只有所有者可以修改。" />
        )
      }
    >
      <form
        className="console-form governance-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (!saving && changed) submit();
        }}
      >
        <Field label="项目名称" htmlFor="governance-name" error={formError} required>
          <Input
            id="governance-name"
            value={name}
            maxLength={100}
            required
            disabled={!canEdit || saving}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field
          label="默认时区"
          htmlFor="governance-timezone"
          hint="IANA 时区，例如 Asia/Shanghai；只影响之后的操作，不改写历史查询。"
          required
        >
          <Input
            id="governance-timezone"
            value={timezone}
            maxLength={100}
            required
            disabled={!canEdit || saving}
            onChange={(event) => setTimezone(event.target.value)}
          />
        </Field>
        {writeError && (
          <div className="inline-notice">
            <Alert tone="error" title={writeError} />
          </div>
        )}
        {canEdit && (
          <div className="inline-actions">
            <Button
              type="submit"
              variant="primary"
              loading={saving}
              disabled={!changed && !pending}
            >
              {pending ? '用原幂等键重试' : '保存设置'}
            </Button>
            <span className="evia-field-hint">设置修订版本：{current.revision}</span>
          </div>
        )}
      </form>
    </Panel>
  );
}
