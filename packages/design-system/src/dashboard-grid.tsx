import { useId, type CSSProperties, type ReactNode } from 'react';
import './dashboard-grid.css';

export interface DashboardLayout {
  x: number;
  y: number;
  width: number;
  height: number;
}

/** Integer geometry only; touching edges are permitted. */
export function dashboardLayoutFits(
  layout: DashboardLayout,
  others: readonly DashboardLayout[],
): boolean {
  const inBounds = (candidate: DashboardLayout) =>
    [candidate.x, candidate.y, candidate.width, candidate.height].every(Number.isInteger) &&
    candidate.x >= 0 &&
    candidate.y >= 0 &&
    candidate.width >= 1 &&
    candidate.width <= 12 &&
    candidate.height >= 1 &&
    candidate.height <= 24 &&
    candidate.x + candidate.width <= 12 &&
    candidate.y + candidate.height <= 1000;

  return (
    inBounds(layout) &&
    others.every(
      (other) =>
        inBounds(other) &&
        (layout.x + layout.width <= other.x ||
          other.x + other.width <= layout.x ||
          layout.y + layout.height <= other.y ||
          other.y + other.height <= layout.y),
    )
  );
}

/** Callers omit the item being edited from others. Invalid edits leave state untouched. */
export function nextDashboardLayout(
  layout: DashboardLayout,
  others: readonly DashboardLayout[],
  patch: Partial<DashboardLayout>,
): DashboardLayout | undefined {
  const next = { ...layout, ...patch };
  return dashboardLayoutFits(next, others) ? next : undefined;
}

export interface DashboardGridItem {
  id: string;
  reading_order: number;
  layout: DashboardLayout;
  content: ReactNode;
}

export interface DashboardGridProps {
  label: string;
  items: readonly DashboardGridItem[];
}

export function DashboardGrid({ label, items }: DashboardGridProps) {
  const ordered = [...items].sort((a, b) => a.reading_order - b.reading_order);
  return (
    <section className="evia-dashboard-grid" aria-label={label}>
      {ordered.map((item) => (
        <div
          className="evia-dashboard-grid-item"
          key={item.id}
          style={
            {
              '--dashboard-column': item.layout.x + 1,
              '--dashboard-row': item.layout.y + 1,
              '--dashboard-width': item.layout.width,
              '--dashboard-height': item.layout.height,
            } as CSSProperties
          }
        >
          {item.content}
        </div>
      ))}
    </section>
  );
}

export interface DashboardCardProps {
  title: ReactNode;
  summary?: ReactNode;
  action?: ReactNode;
  children: ReactNode;
}

export function DashboardCard({ title, summary, action, children }: DashboardCardProps) {
  const titleID = useId();
  return (
    <section className="evia-dashboard-card" aria-labelledby={titleID}>
      <header className="evia-dashboard-card-header">
        <div className="evia-dashboard-card-heading">
          <h2 id={titleID}>{title}</h2>
          {summary != null && <div className="evia-dashboard-card-summary">{summary}</div>}
        </div>
        {action != null && <div className="evia-dashboard-card-actions">{action}</div>}
      </header>
      <div
        className="evia-dashboard-card-content"
        tabIndex={0}
        role="region"
        aria-labelledby={titleID}
      >
        {children}
      </div>
    </section>
  );
}
