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

export function AuthLayout({
  children,
  onNavigate,
  topAction,
}: {
  children: ReactNode;
  onNavigate?: (href: string) => void;
  topAction?: ReactNode;
}) {
  return (
    <div className="auth-layout">
      <div className="auth-pane">
        <header className="auth-header">
          <Brand href="/" onNavigate={onNavigate} />
          {topAction ? <div className="auth-header-action">{topAction}</div> : null}
        </header>
        <main className="auth-main">
          <div className="auth-form">{children}</div>
        </main>
        <footer className="auth-footnote">
          <span>Evia 映途 · 自托管实时产品分析平台</span>
        </footer>
      </div>
      <aside className="auth-showcase" aria-hidden="true">
        <img
          className="auth-showcase-poster"
          src="/media/auth-studio-poster.jpg"
          alt=""
          loading="eager"
          decoding="async"
        />
        <video
          className="auth-showcase-video"
          src="/media/auth-studio.mp4"
          poster="/media/auth-studio-poster.jpg"
          autoPlay
          muted
          loop
          playsInline
          preload="metadata"
        />
        <div className="auth-showcase-scrim" />
        <div className="auth-showcase-credit">Evia 映途 · 私有化事件与产品分析</div>
      </aside>
    </div>
  );
}

export function WorkspaceShell({
  children,
  account,
  onOpenAccount,
  onLogout,
  busy,
  onNavigate,
  navigation,
  projectContext,
}: {
  children: ReactNode;
  account: string;
  onOpenAccount?: () => void;
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
  const activeGroup = groups.find((group) => group.items.some((item) => item.active)) ?? groups[0];
  return (
    <div className="workspace">
      <a className="skip-link" href="#main-content">
        跳到主要内容
      </a>
      <header className="workspace-header">
        <Brand onNavigate={onNavigate} />
        <nav className="workspace-primary-nav" aria-label="工作区分类">
          {groups.map((group) => {
            const destination = group.items[0];
            if (!destination) return null;
            const active = group === activeGroup;
            return (
              <a
                key={group.label}
                href={destination.href}
                aria-current={active ? 'location' : undefined}
                onClick={(event) => navigate(event, destination.href, onNavigate)}
              >
                {group.label}
              </a>
            );
          })}
        </nav>
        <div className="workspace-utilities">
          <div className="account-actions" aria-label={`当前账号：${account}`}>
            {onOpenAccount ? (
              <button
                type="button"
                className="account-trigger"
                onClick={onOpenAccount}
                aria-label="账号设置"
                title="账号设置"
              >
                <span className="account-avatar" aria-hidden="true">
                  {account.slice(0, 1).toUpperCase()}
                </span>
                <span className="account-name">{account}</span>
              </button>
            ) : (
              <>
                <span className="account-avatar" title={account} aria-hidden="true">
                  {account.slice(0, 1).toUpperCase()}
                </span>
                <span className="account-name" title={account}>
                  {account}
                </span>
              </>
            )}
            <Button
              className="workspace-logout"
              variant="ghost"
              loading={busy}
              onClick={onLogout}
              aria-label="退出登录"
            >
              <svg width="19" height="19" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                <path
                  d="M10 5H6.8A2.8 2.8 0 0 0 4 7.8v8.4A2.8 2.8 0 0 0 6.8 19H10m5-4 3-3m0 0-3-3m3 3H9"
                  stroke="currentColor"
                  strokeWidth="1.7"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                />
              </svg>
              <span className="sr-only">退出登录</span>
            </Button>
          </div>
          <Button
            className="workspace-nav-toggle"
            variant="secondary"
            aria-label={navigationOpen ? '关闭工作区菜单' : '打开工作区菜单'}
            aria-expanded={navigationOpen}
            aria-controls="workspace-navigation"
            onClick={() => setNavigationOpen((open) => !open)}
          >
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" aria-hidden="true">
              {navigationOpen ? (
                <path
                  d="m6 6 12 12M18 6 6 18"
                  stroke="currentColor"
                  strokeWidth="1.8"
                  strokeLinecap="round"
                />
              ) : (
                <path
                  d="M5 7h14M5 12h14M5 17h14"
                  stroke="currentColor"
                  strokeWidth="1.8"
                  strokeLinecap="round"
                />
              )}
            </svg>
          </Button>
        </div>
      </header>
      <div className="workspace-body">
        <aside className="workspace-nav" data-mobile-open={navigationOpen || undefined}>
          <nav id="workspace-navigation" aria-label="功能导航">
            {groups.map((group) => (
              <div
                className="workspace-nav-group"
                role="group"
                aria-label={group.label}
                data-active={group === activeGroup || undefined}
                key={group.label}
              >
                <h2>{group.label}</h2>
                <div className="workspace-nav-links">
                  {group.items.map((item) => (
                    <a
                      key={item.href}
                      href={item.href}
                      aria-label={item.label}
                      aria-current={item.active ? 'page' : undefined}
                      data-label={item.label}
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
                      <span className="workspace-nav-label">{item.label}</span>
                    </a>
                  ))}
                </div>
              </div>
            ))}
          </nav>
        </aside>
        <main id="main-content" tabIndex={-1}>
          {projectContext && <div className="workspace-context">{projectContext}</div>}
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

export function PageHeading({
  title,
  summary,
  action,
  className = '',
}: {
  title: string;
  summary?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <header className={`page-heading ${className}`}>
      <div className="page-heading-copy">
        <h1>{title}</h1>
        {summary && <p className="page-heading-summary">{summary}</p>}
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
