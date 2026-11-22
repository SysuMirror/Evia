import { useState, type MouseEvent, type ReactNode } from 'react';
import { Button } from './controls';
import { Alert } from './feedback';

function navigate(
  event: MouseEvent<HTMLAnchorElement>,
  href: string,
  onNavigate?: (href: string) => void,
) {
  if (
    !onNavigate ||
    event.defaultPrevented ||
    event.button !== 0 ||
    event.metaKey ||
    event.ctrlKey ||
    event.shiftKey ||
    event.altKey
  )
    return;
  event.preventDefault();
  onNavigate(href);
}

export function Brand({
  href = '/projects',
  onNavigate,
}: {
  href?: string;
  onNavigate?: (href: string) => void;
}) {
  return (
    <a
      className="brand"
      href={href}
      aria-label="Evia 映途"
      onClick={(event) => navigate(event, href, onNavigate)}
    >
      <img className="brand-icon" src="/evia-icon.png" alt="" width={28} height={28} />
      <span>
        Evia <span className="brand-name">映途</span>
      </span>
    </a>
  );
}

export function AuthLayout({ children }: { children: ReactNode }) {
  return (
    <div className="auth-layout">
      <header className="auth-header">
        <Brand href="/login" />
      </header>
      <main className="auth-main">
        <div className="auth-content">
          <section className="auth-intro" aria-label="Evia">
            <h2>连接事件与业务</h2>
            <div className="auth-visual" aria-hidden="true">
              <span />
              <span />
              <span />
            </div>
          </section>
          <div className="auth-form">{children}</div>
        </div>
      </main>
    </div>
  );
}

export function WorkspaceShell({
  children,
  account,
  onLogout,
  busy,
  onNavigate,
  navigation,
  projectContext,
}: {
  children: ReactNode;
  account: string;
  onLogout: () => void;
  busy: boolean;
  onNavigate?: (href: string) => void;
  navigation?: readonly WorkspaceNavigationItem[];
  projectContext?: ReactNode;
}) {
  const [navigationOpen, setNavigationOpen] = useState(false);
  const items = navigation ?? [defaultNavigation];
  const groups = items.reduce<{ label: string; items: WorkspaceNavigationItem[] }[]>(
    (all, item) => {
      const label = item.group ?? '工作区';
      const current = all.find((candidate) => candidate.label === label);
      if (current) current.items.push(item);
      else all.push({ label, items: [item] });
      return all;
    },
    [],
  );
  return (
    <div className="workspace">
      <a className="skip-link" href="#main-content">
        跳到主要内容
      </a>
      <header className="workspace-header">
        <Brand onNavigate={onNavigate} />
        <div className="account-actions">
          <span className="account-avatar" aria-hidden="true">
            {account.slice(0, 1).toUpperCase()}
          </span>
          <span className="account-name" title={account}>
            {account}
          </span>
          <Button variant="ghost" loading={busy} onClick={onLogout} aria-label="退出登录">
            退出登录
          </Button>
        </div>
      </header>
      <div className="workspace-body">
        <aside className="workspace-nav">
          {projectContext && <div className="workspace-context">{projectContext}</div>}
          <Button
            className="workspace-nav-toggle"
            variant="secondary"
            aria-expanded={navigationOpen}
            aria-controls="workspace-navigation"
            onClick={() => setNavigationOpen((open) => !open)}
          >
            <span>工作区导航</span>
            <span aria-hidden="true">{navigationOpen ? '收起' : '展开'}</span>
          </Button>
          <nav
            id="workspace-navigation"
            aria-label="主导航"
            data-mobile-open={navigationOpen || undefined}
          >
            {groups.map((group) => (
              <div
                className="workspace-nav-group"
                role="group"
                aria-label={group.label}
                key={group.label}
              >
                <h2>{group.label}</h2>
                <div className="workspace-nav-links">
                  {group.items.map((item) => (
                    <a
                      key={item.href}
                      href={item.href}
                      aria-current={item.active ? 'page' : undefined}
                      onClick={(event) => {
                        navigate(event, item.href, onNavigate);
                        if (event.defaultPrevented) setNavigationOpen(false);
                      }}
                    >
                      {item.icon && (
                        <span className="nav-icon" aria-hidden="true">
                          {item.icon}
                        </span>
                      )}
                      <span>{item.label}</span>
                    </a>
                  ))}
                </div>
              </div>
            ))}
          </nav>
        </aside>
        <main id="main-content" tabIndex={-1}>
          {children}
        </main>
      </div>
    </div>
  );
}

export interface WorkspaceNavigationItem {
  href: string;
  label: string;
  active?: boolean;
  icon?: ReactNode;
  group?: string;
}

const defaultNavigation: WorkspaceNavigationItem = {
  href: '/projects',
  label: '项目管理',
  active: true,
  icon: (
    <svg
      className="nav-icon"
      width="18"
      height="18"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      aria-hidden="true"
    >
      <rect x="3.5" y="3.5" width="17" height="17" rx="3" />
      <path d="M3.5 9h17M9 9v11.5" />
    </svg>
  ),
};

export function PageHeading({ title, action }: { title: string; action?: ReactNode }) {
  return (
    <header className="page-heading">
      <div className="page-heading-copy">
        <h1>{title}</h1>
      </div>
      {action && <div className="heading-actions">{action}</div>}
    </header>
  );
}

export function DataSurface({
  label,
  children,
  footer,
}: {
  label: string;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <section className="data-surface" aria-label={label}>
      {children}
      {footer}
    </section>
  );
}

function PaginationArrow({ direction }: { direction: 'previous' | 'next' }) {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path
        d={direction === 'previous' ? 'M10 3.5 5.5 8l4.5 4.5' : 'M6 3.5 10.5 8 6 12.5'}
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export function PaginationBar({
  page,
  count,
  hasNext,
  busy,
  onPrevious,
  onNext,
}: {
  page: number;
  count: number;
  hasNext: boolean;
  busy: boolean;
  onPrevious: () => void;
  onNext: () => void;
}) {
  return (
    <nav className="pagination-bar" aria-label="分页" aria-busy={busy || undefined}>
      <span className="pagination-summary" aria-live="polite">
        第 {page} 页，本页 {count} 项
      </span>
      <div className="pagination-actions">
        <Button
          size="small"
          startIcon={<PaginationArrow direction="previous" />}
          disabled={page === 1 || busy}
          onClick={onPrevious}
        >
          上一页
        </Button>
        <Button
          size="small"
          endIcon={<PaginationArrow direction="next" />}
          disabled={!hasNext || busy}
          onClick={onNext}
        >
          下一页
        </Button>
      </div>
    </nav>
  );
}

export function LoadingPanel({ label = '正在加载' }: { label?: string }) {
  return (
    <div className="loading-panel" role="status" aria-label={label}>
      <span className="evia-spinner" aria-hidden="true" />
      <span>{label}</span>
    </div>
  );
}

export function ErrorPanel({
  message,
  requestID,
  retry,
}: {
  message: string;
  requestID?: string;
  retry?: () => void;
}) {
  return (
    <Alert
      tone="error"
      title={message}
      action={
        retry ? (
          <Button size="small" onClick={retry}>
            重试
          </Button>
        ) : undefined
      }
    >
      {requestID ? <span className="request-reference">请求编号：{requestID}</span> : undefined}
    </Alert>
  );
}

export function EmptyPanel({ description, action }: { description: string; action?: ReactNode }) {
  return (
    <div className="empty-panel">
      <p>{description}</p>
      {action}
    </div>
  );
}
