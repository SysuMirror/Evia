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
        <div className="auth-showcase-bg">
          <svg
            className="auth-showcase-art"
            viewBox="0 0 640 960"
            fill="none"
            preserveAspectRatio="xMidYMid slice"
          >
            <defs>
              <radialGradient id="evia-auth-glow-a" cx="72%" cy="22%" r="58%">
                <stop offset="0%" stopColor="#8fa2ff" stopOpacity="0.58" />
                <stop offset="45%" stopColor="#5264ff" stopOpacity="0.24" />
                <stop offset="100%" stopColor="#0d1326" stopOpacity="0" />
              </radialGradient>
              <radialGradient id="evia-auth-glow-b" cx="24%" cy="74%" r="54%">
                <stop offset="0%" stopColor="#d99a82" stopOpacity="0.34" />
                <stop offset="50%" stopColor="#4f6096" stopOpacity="0.18" />
                <stop offset="100%" stopColor="#0b1020" stopOpacity="0" />
              </radialGradient>
              <linearGradient id="evia-auth-ribbon-1" x1="0%" y1="0%" x2="100%" y2="100%">
                <stop offset="0%" stopColor="#a7b8ff" stopOpacity="0.55" />
                <stop offset="52%" stopColor="#6374ff" stopOpacity="0.22" />
                <stop offset="100%" stopColor="#e2b49c" stopOpacity="0.08" />
              </linearGradient>
              <linearGradient id="evia-auth-ribbon-2" x1="100%" y1="15%" x2="10%" y2="95%">
                <stop offset="0%" stopColor="#f0c8b4" stopOpacity="0.32" />
                <stop offset="55%" stopColor="#6c80ff" stopOpacity="0.2" />
                <stop offset="100%" stopColor="#1b2644" stopOpacity="0" />
              </linearGradient>
              <linearGradient id="evia-auth-ridge" x1="0%" y1="0%" x2="0%" y2="100%">
                <stop offset="0%" stopColor="#1f2c4d" stopOpacity="0.72" />
                <stop offset="100%" stopColor="#090e1c" stopOpacity="0.96" />
              </linearGradient>
            </defs>
            <rect width="640" height="960" fill="#0d1326" />
            <rect width="640" height="960" fill="url(#evia-auth-glow-a)" />
            <rect width="640" height="960" fill="url(#evia-auth-glow-b)" />
            <path
              d="M-60 330 C 120 220, 290 420, 480 270 C 570 200, 650 185, 720 220 L 720 440 C 570 390, 420 510, 230 430 C 90 370, 0 430, -60 470 Z"
              fill="url(#evia-auth-ribbon-1)"
            />
            <path
              d="M-40 560 C 140 445, 310 610, 520 470 C 600 418, 660 420, 720 460 L 720 680 C 560 630, 390 730, 190 660 C 70 618, 0 650, -40 690 Z"
              fill="url(#evia-auth-ribbon-2)"
            />
            <path
              d="M-40 720 Q 140 620, 320 675 T 680 610 L 680 980 L -40 980 Z"
              fill="url(#evia-auth-ridge)"
            />
            <g stroke="#ffffff" strokeOpacity="0.07" strokeWidth="1">
              <circle cx="490" cy="210" r="150" />
              <circle cx="490" cy="210" r="235" />
              <circle cx="140" cy="650" r="190" />
            </g>
          </svg>
        </div>
        <div className="auth-showcase-inner">
          <div className="auth-showcase-pill">
            <span className="auth-showcase-dot" />
            <span>自托管 · 全链路私有部署</span>
          </div>
          <div className="auth-showcase-card">
            <div className="auth-showcase-card-head">
              <div>
                <div className="auth-showcase-label">实时事件吞吐</div>
                <div className="auth-showcase-value">248,920</div>
              </div>
              <span className="auth-showcase-delta">+18.4%</span>
            </div>
            <svg className="auth-showcase-spark" viewBox="0 0 420 80" fill="none">
              <defs>
                <linearGradient id="evia-spark-fill" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="#8fa2ff" stopOpacity="0.4" />
                  <stop offset="100%" stopColor="#8fa2ff" stopOpacity="0" />
                </linearGradient>
              </defs>
              <path
                d="M4 64 C 46 57, 74 46, 110 49 C 148 52, 180 28, 224 33 C 266 38, 300 18, 344 21 C 378 23, 396 12, 412 10 L 412 78 L 4 78 Z"
                fill="url(#evia-spark-fill)"
              />
              <path
                d="M4 64 C 46 57, 74 46, 110 49 C 148 52, 180 28, 224 33 C 266 38, 300 18, 344 21 C 378 23, 396 12, 412 10"
                stroke="#b8c4ff"
                strokeWidth="2.4"
                strokeLinecap="round"
              />
              <circle cx="412" cy="10" r="4.5" fill="#ffffff" />
            </svg>
            <div className="auth-showcase-metrics">
              <div>
                <span>漏斗转化</span>
                <strong>42.6%</strong>
              </div>
              <div>
                <span>次日留存</span>
                <strong>61.8%</strong>
              </div>
              <div>
                <span>数据出境</span>
                <strong>0 字节</strong>
              </div>
            </div>
          </div>
          <div className="auth-showcase-caption">
            <h2>连接事件、路径与业务增长</h2>
            <p>从实时埋点采集到多维洞察与合规治理，一切尽在掌控。</p>
          </div>
        </div>
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
