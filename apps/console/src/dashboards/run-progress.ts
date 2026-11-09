import type { DashboardRun } from '@evia/api-client';
const active = (state: string) => state === 'queued' || state === 'running';
/** An older result read must not undo a terminal status already confirmed by cancellation. */
export function retainDashboardProgress(
  previous: DashboardRun,
  incoming: DashboardRun,
): DashboardRun {
  if (previous.run_id !== incoming.run_id) return incoming;
  let retained = false;
  const widgets = incoming.widgets.map((widget) => {
    const confirmed = previous.widgets.find(
      (w) => w.widget_id === widget.widget_id && w.query.query_id === widget.query.query_id,
    );
    if (
      confirmed &&
      ((!active(confirmed.query.state) && active(widget.query.state)) ||
        (confirmed.query.state === 'expired' && widget.query.state !== 'expired'))
    ) {
      retained = true;
      return confirmed;
    }
    return widget;
  });
  if (!retained) return incoming;
  const states = widgets.map((w) => w.query.state);
  const state: DashboardRun['state'] = states.includes('running')
    ? 'running'
    : states.includes('queued')
      ? 'queued'
      : states.every((s) => s === 'succeeded')
        ? 'succeeded'
        : states.includes('succeeded')
          ? 'partial'
          : states.every((s) => s === 'canceled')
            ? 'canceled'
            : states.every((s) => s === 'expired')
              ? 'expired'
              : 'failed';
  return { ...incoming, widgets, state };
}
