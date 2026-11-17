import { useEffect, useState } from 'react';
import { Field, Select } from '@evia/design-system';
import { formatTime } from '../analysis-model';
import { AUTO_REFRESH_OPTIONS } from './use-auto-refresh';

type AutoRefreshControlsProps = {
  id: string;
  intervalSeconds: number;
  nextAt: number | null;
  disabled: boolean;
  timezone: string;
  onChange: (intervalSeconds: number) => void;
};

export function AutoRefreshControls({
  id,
  intervalSeconds,
  nextAt,
  disabled,
  timezone,
  onChange,
}: AutoRefreshControlsProps) {
  // A light 1s ticker drives the "next run" label without re-rendering the
  // whole dashboard grid.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!intervalSeconds || !nextAt) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [intervalSeconds, nextAt]);

  const enabled = intervalSeconds > 0;
  const hint = !enabled
    ? '运行看板后可开启自动刷新；最短间隔 60 秒，隐藏标签页时自动暂停。'
    : document.hidden
      ? '标签页隐藏中，自动刷新已暂停，回到页面后恢复计时。'
      : nextAt
        ? `下次自动运行约 ${formatTime(new Date(nextAt).toISOString(), timezone)}（约 ${Math.max(0, Math.ceil((nextAt - now) / 1000))} 秒后）。`
        : '正在安排下一次自动运行。';

  return (
    <div className="dashboard-auto-refresh" aria-label="自动刷新设置">
      <Field label="自动刷新" htmlFor={`${id}-auto-refresh`} hint={hint}>
        <Select
          id={`${id}-auto-refresh`}
          value={String(intervalSeconds)}
          disabled={disabled}
          options={AUTO_REFRESH_OPTIONS.map((option) => ({
            value: String(option.value),
            label: option.label,
          }))}
          onChange={(event) => onChange(Number(event.target.value))}
        />
      </Field>
    </div>
  );
}
