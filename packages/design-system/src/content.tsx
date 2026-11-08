import { useId, useRef, type ReactNode } from 'react';

export function Toolbar({
  children,
  actions,
  label,
}: {
  children?: ReactNode;
  actions?: ReactNode;
  label?: string;
}) {
  return (
    <div className="evia-toolbar" role={label ? 'group' : undefined} aria-label={label}>
      <div className="evia-toolbar-controls">{children}</div>
      {actions && <div className="inline-actions">{actions}</div>}
    </div>
  );
}

export function Panel({
  children,
  title,
  action,
  label,
}: {
  children: ReactNode;
  title?: ReactNode;
  action?: ReactNode;
  label?: string;
}) {
  const titleID = useId();
  return (
    <section
      className="evia-panel"
      aria-label={label}
      aria-labelledby={title ? titleID : undefined}
    >
      {(title || action) && (
        <header className="evia-panel-heading">
          {title && <h2 id={titleID}>{title}</h2>}
          {action && <div className="inline-actions">{action}</div>}
        </header>
      )}
      {children}
    </section>
  );
}

export function CodeBlock({ children, label }: { children: string; label?: string }) {
  return (
    <pre className="code-block" tabIndex={0} role={label ? 'region' : undefined} aria-label={label}>
      <code>{children}</code>
    </pre>
  );
}

export interface TabItem {
  id: string;
  label: ReactNode;
  content: ReactNode;
  disabled?: boolean;
}

export function Tabs({
  label,
  items,
  value,
  onChange,
}: {
  label: string;
  items: readonly TabItem[];
  value: string;
  onChange: (id: string) => void;
}) {
  const uniqueID = useId();
  const buttons = useRef(new Map<string, HTMLButtonElement>());
  const enabled = items.filter((item) => !item.disabled);
  const selected = enabled.find((item) => item.id === value) ?? enabled[0];
  return (
    <div className="evia-tabs">
      <div className="evia-tab-list" role="tablist" aria-label={label}>
        {items.map((item, index) => (
          <button
            key={item.id}
            ref={(node) => {
              if (node) buttons.current.set(item.id, node);
              else buttons.current.delete(item.id);
            }}
            type="button"
            role="tab"
            id={`${uniqueID}-tab-${index}`}
            aria-controls={`${uniqueID}-panel-${index}`}
            aria-selected={selected?.id === item.id}
            tabIndex={selected?.id === item.id ? 0 : -1}
            disabled={item.disabled}
            onClick={() => onChange(item.id)}
            onKeyDown={(event) => {
              if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return;
              const current = enabled.findIndex((candidate) => candidate.id === item.id);
              let next: TabItem | undefined;
              if (event.key === 'ArrowRight') next = enabled[(current + 1) % enabled.length];
              else if (event.key === 'ArrowLeft')
                next = enabled[(current - 1 + enabled.length) % enabled.length];
              else if (event.key === 'Home') next = enabled[0];
              else if (event.key === 'End') next = enabled.at(-1);
              if (!next) return;
              event.preventDefault();
              buttons.current.get(next.id)?.focus();
              onChange(next.id);
            }}
          >
            {item.label}
          </button>
        ))}
      </div>
      {items.map((item, index) => (
        <div
          key={item.id}
          id={`${uniqueID}-panel-${index}`}
          role="tabpanel"
          aria-labelledby={`${uniqueID}-tab-${index}`}
          tabIndex={0}
          hidden={selected?.id !== item.id}
          className="evia-tab-panel"
        >
          {item.content}
        </div>
      ))}
    </div>
  );
}
