import { useId } from 'react';
import type { SavedAnalysisExecute, SavedAnalysisVersion } from '@evia/api-client';
import { Button, Field, Input, Modal, Select } from '@evia/design-system';
import type { SavedRunRange } from './types';

const zones = [...new Set(['UTC', 'Asia/Shanghai', ...Intl.supportedValuesOf('timeZone')])];

export function savedExecutionBody(
  range: SavedRunRange,
  kind: SavedAnalysisVersion['definition']['query']['kind'],
): SavedAnalysisExecute {
  const from = new Date(`${range.from}Z`),
    to = new Date(`${range.to}Z`);
  const maximum = kind === 'funnel' ? 7 : 31;
  if (
    !Number.isFinite(from.getTime()) ||
    !Number.isFinite(to.getTime()) ||
    from >= to ||
    to.getTime() - from.getTime() > maximum * 86400000 ||
    from.getUTCFullYear() < 1970 ||
    to.getUTCFullYear() > 2100
  )
    throw new Error(`请选择开始早于结束的有效时间范围，最多 ${maximum} 天，年份为 1970 至 2100。`);
  try {
    new Intl.DateTimeFormat('zh-CN', { timeZone: range.timezone });
  } catch {
    throw new Error('请选择有效的 IANA 时区。');
  }
  return {
    time_range: { from: from.toISOString(), to: to.toISOString() },
    timezone: range.timezone,
  };
}

export function SavedTimeControls({
  range,
  setRange,
  rangeOpen,
  rangeError,
  setRangeError,
  kind,
  onClose,
}: {
  range: SavedRunRange;
  setRange: (range: SavedRunRange) => void;
  rangeOpen: boolean;
  rangeError: string;
  setRangeError: (message: string) => void;
  kind: SavedAnalysisVersion['definition']['query']['kind'];
  onClose: () => void;
}) {
  const id = useId();
  return (
    <Modal
      open={rangeOpen}
      title="执行时间范围"
      onRequestClose={() => onClose()}
      footer={
        <Button variant="primary" type="submit" form={`${id}-range`}>
          完成
        </Button>
      }
    >
      <form
        id={`${id}-range`}
        className="section-stack"
        aria-describedby={rangeError ? `${id}-error` : undefined}
        onSubmit={(event) => {
          event.preventDefault();
          try {
            savedExecutionBody(range, kind);
            setRangeError('');
            onClose();
          } catch (failure) {
            setRangeError((failure as Error).message);
          }
        }}
      >
        {rangeError && (
          <p id={`${id}-error`} role="alert">
            {rangeError}
          </p>
        )}
        <div className="inline-actions">
          {[
            { label: '最近 1 小时', hours: 1 },
            { label: '最近 24 小时', hours: 24 },
            { label: '最近 7 天', hours: 168 },
          ].map((preset) => (
            <Button
              key={preset.hours}
              onClick={() => {
                const to = new Date();
                to.setUTCSeconds(0, 0);
                setRange({
                  ...range,
                  from: new Date(to.getTime() - preset.hours * 3600000).toISOString().slice(0, 16),
                  to: to.toISOString().slice(0, 16),
                });
                setRangeError('');
              }}
            >
              {preset.label}
            </Button>
          ))}
        </div>
        <div className="field-grid">
          <Field label="开始时间（UTC）" htmlFor={`${id}-from`}>
            <Input
              id={`${id}-from`}
              type="datetime-local"
              required
              min="1970-01-01T00:00"
              max="2100-12-31T23:59"
              value={range.from}
              onChange={(event) => {
                setRange({ ...range, from: event.target.value });
                setRangeError('');
              }}
            />
          </Field>
          <Field label="结束时间（UTC，不含）" htmlFor={`${id}-to`}>
            <Input
              id={`${id}-to`}
              type="datetime-local"
              required
              min="1970-01-01T00:00"
              max="2100-12-31T23:59"
              value={range.to}
              onChange={(event) => {
                setRange({ ...range, to: event.target.value });
                setRangeError('');
              }}
            />
          </Field>
        </div>
        <Field label="统计和显示时区" htmlFor={`${id}-zone`}>
          <Select
            id={`${id}-zone`}
            value={range.timezone}
            onChange={(event) => setRange({ ...range, timezone: event.target.value })}
            options={zones.map((zone) => ({ value: zone, label: zone }))}
          />
        </Field>
        <p className="field-help">时间范围左闭右开，输入采用 UTC。调整时间范围不会执行查询。</p>
      </form>
    </Modal>
  );
}
