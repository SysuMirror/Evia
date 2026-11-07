import type { ReactNode } from 'react';
import { App, Alert, Button, ConfigProvider, Empty, Spin } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import './styles.css';

export { Alert, App, Button, Card, Descriptions, Form, Input, Modal, Select, Space, Table, Tag, Typography } from 'antd';
export type { TableColumnsType } from 'antd';

export function DesignProvider({ children }: { children: ReactNode }) {
 return <ConfigProvider locale={zhCN} theme={{ token: {
  colorPrimary: '#25655b', colorInfo: '#25655b', colorText: '#243832', colorTextSecondary: '#66776f',
  colorBgLayout: '#f4f6f3', colorBorder: '#dbe2dc', borderRadius: 8, fontSize: 14,
  fontFamily: 'Inter, -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif',
  controlHeight: 40,
 } }}><App>{children}</App></ConfigProvider>;
}

export function WorkspaceShell({ children, account, onLogout, busy }: {
 children: ReactNode; account: string; onLogout: () => void; busy: boolean;
}) {
 return <div className="workspace">
  <a className="skip-link" href="#main-content">跳到主要内容</a>
  <header className="workspace-header">
   <a className="brand" href="/projects"><span className="brand-mark" aria-hidden="true">e</span><span>Evia <small>映途</small></span></a>
   <div className="account-actions"><span>{account}</span><Button loading={busy} onClick={onLogout}>退出登录</Button></div>
  </header>
  <div className="workspace-body">
   <aside className="workspace-nav"><span className="nav-label">工作空间</span><a href="/projects" aria-current="page">项目管理</a><p>按业务划分项目，管理独立的事件和访问范围。</p></aside>
   <main id="main-content" tabIndex={-1}>{children}</main>
  </div>
 </div>;
}

export function PageHeading({ title, description, action, eyebrow }: {
 title: string; description: string; action?: ReactNode; eyebrow?: string;
}) {
 return <div className="page-heading"><div>{eyebrow && <span className="eyebrow">{eyebrow}</span>}<h1>{title}</h1><p>{description}</p></div>{action && <div className="heading-action">{action}</div>}</div>;
}
export function LoadingPanel({ label = '正在加载' }: { label?: string }) {
 return <div className="loading-panel" role="status" aria-label={label}><Spin /><span>{label}</span></div>;
}
export function ErrorPanel({ message, requestID, retry }: { message: string; requestID?: string; retry?: () => void }) {
 return <Alert type="error" showIcon title={message} description={requestID ? <span className="request-reference">请求编号：{requestID}</span> : undefined} action={retry ? <Button size="small" onClick={retry}>重试</Button> : undefined} />;
}
export function EmptyPanel({ description, action }: { description: string; action?: ReactNode }) {
 return <div className="empty-panel"><Empty description={description} image={Empty.PRESENTED_IMAGE_SIMPLE}>{action}</Empty></div>;
}
