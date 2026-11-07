import type { ReactNode } from 'react';
import { App, Alert, Button, ConfigProvider, Empty, Spin } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import './styles.css';

export {
  Alert,
  App,
  Button,
  Card,
  Descriptions,
  Form,
  Input,
  Modal,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd';
export type { TableColumnsType } from 'antd';

export function DesignProvider({ children }: { children: ReactNode }) {
  return (
    <ConfigProvider
      locale={zhCN}
      button={{ autoInsertSpace: false }}
      theme={{
        token: {
          colorPrimary: '#3561d8',
          colorInfo: '#3561d8',
          colorText: '#1f2937',
          colorTextSecondary: '#64748b',
          colorBgLayout: '#f5f6f8',
          colorBorder: '#dce1e8',
          borderRadius: 6,
          fontSize: 14,
          fontFamily:
            'Inter, -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif',
          controlHeight: 34,
        },
        components: {
          Table: {
            headerBg: '#f7f8fa',
            headerColor: '#64748b',
            headerSplitColor: 'transparent',
            cellPaddingBlock: 14,
            cellPaddingInline: 20,
          },
          Modal: { titleFontSize: 18, padding: 24 },
          Form: { itemMarginBottom: 20, labelColor: '#334155' },
          Button: { defaultShadow: 'none', primaryShadow: 'none', fontWeight: 500 },
        },
      }}
    >
      <App>{children}</App>
    </ConfigProvider>
  );
}

export function Brand({ href = '/projects' }: { href?: string }) {
  return (
    <a className="brand" href={href} aria-label="Evia 映途">
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
}: {
  children: ReactNode;
  account: string;
  onLogout: () => void;
  busy: boolean;
}) {
  return (
    <div className="workspace">
      <a className="skip-link" href="#main-content">
        跳到主要内容
      </a>
      <header className="workspace-header">
        <Brand />
        <div className="account-actions">
          <span className="account-avatar" aria-hidden="true">
            {account.slice(0, 1).toUpperCase()}
          </span>
          <span className="account-name" title={account}>
            {account}
          </span>
          <Button
            type="text"
            loading={busy}
            onClick={onLogout}
            aria-label="退出登录"
            aria-busy={busy}
          >
            退出登录
          </Button>
        </div>
      </header>
      <div className="workspace-body">
        <aside className="workspace-nav">
          <nav aria-label="主导航">
            <a href="/projects" aria-current="page">
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
              项目管理
            </a>
          </nav>
        </aside>
        <main id="main-content" tabIndex={-1}>
          {children}
        </main>
      </div>
    </div>
  );
}

export function PageHeading({ title, action }: { title: string; action?: ReactNode }) {
  return (
    <div className="page-heading">
      <h1>{title}</h1>
      {action && <div className="heading-actions">{action}</div>}
    </div>
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
    <div className="pagination-bar">
      <span className="pagination-summary">
        第 {page} 页，本页 {count} 项
      </span>
      <div className="pagination-actions">
        <Button disabled={page === 1 || busy} onClick={onPrevious}>
          上一页
        </Button>
        <Button disabled={!hasNext || busy} onClick={onNext}>
          下一页
        </Button>
      </div>
    </div>
  );
}
export function LoadingPanel({ label = '正在加载' }: { label?: string }) {
  return (
    <div className="loading-panel" role="status" aria-label={label}>
      <Spin />
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
      type="error"
      showIcon
      title={message}
      description={
        requestID ? <span className="request-reference">请求编号：{requestID}</span> : undefined
      }
      action={
        retry ? (
          <Button size="small" onClick={retry}>
            重试
          </Button>
        ) : undefined
      }
    />
  );
}
export function EmptyPanel({ description, action }: { description: string; action?: ReactNode }) {
  return (
    <div className="empty-panel">
      <Empty description={description} image={Empty.PRESENTED_IMAGE_SIMPLE}>
        {action}
      </Empty>
    </div>
  );
}
