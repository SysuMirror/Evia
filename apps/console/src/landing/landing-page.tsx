import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import './landing-page.css';

interface CapabilityItem {
  id: string;
  title: string;
  summary: string;
  panelTitle: string;
  panelNote: string;
  rows?: { label: string; value: string; detail: string }[];
  code?: string;
}

const CAPABILITIES: CapabilityItem[] = [
  {
    id: 'analytics',
    title: '事件、漏斗与留存分析',
    summary:
      '支持事件趋势、有序转化漏斗、留存队列、视口点击热力图与复合公式指标，常用分析可固定版本并编排到自定义看板。',
    panelTitle: '分析能力覆盖',
    panelNote: '查询任务与版本化分析口径保持一致，避免团队间口径漂移。',
    rows: [
      { label: '事件与趋势', value: '计数 / 去重 UV / 属性分组', detail: '支持按环境与时区聚合' },
      { label: '有序漏斗', value: '多步转化窗口', detail: '逐步骤展示留存与流失比例' },
      { label: '留存与行为链', value: '队列留存 / 原始路径', detail: '支持长链路游标续查' },
      { label: '指标与看板', value: '独立指标 / 复合公式', detail: '多图固定版本统一运行' },
    ],
  },
  {
    id: 'contracts',
    title: '强类型契约与多环境发布',
    summary:
      '区分开发、预发与生产环境管理事件定义，经交付探针核验后不可变发布，并可导出 TypeScript 客户端契约产物。',
    panelTitle: '浏览器 SDK 显式上报',
    panelNote: '零运行时依赖，自动校验已发布事件配置，支持有界队列与不可变重试。',
    code: `import { createEviaClient } from '@evia/browser-sdk';

const evia = createEviaClient({
  endpoint: 'https://evia.internal',
  sourceKey: 'src_live_9f4c2a',
});

evia.emit('checkout_completed', {
  plan: 'team',
  amount_cents: 19900,
  currency: 'CNY',
});`,
  },
  {
    id: 'replay',
    title: '加密会话回放与身份修订',
    summary:
      '结合 RSA 公私钥隔离敏感回放内容，支持匿名访客与登录账号的身份修订及历史快照审计，把抽象指标还原为真实交互。',
    panelTitle: '隐私隔离与身份边界',
    panelNote: '客户端在离开浏览器前完成敏感载荷加密，私钥不进入采集路径。',
    rows: [
      { label: '内容加密', value: 'RSA 公私钥隔离', detail: '敏感内容仅授权主体可解密查看' },
      { label: '回放采集', value: '采样优先 + 增量快照', detail: '受单会话预算约束，防止体积膨胀' },
      { label: '身份修订', value: '匿名与账号归并', detail: '保留历史修订快照与审计记录' },
    ],
  },
  {
    id: 'governance',
    title: '细粒度治理与数据生命周期',
    summary:
      '提供项目成员角色与一次性邀请链接、独立机器凭据、合规数据删除、可验证归档回补及异步导出，满足长期治理要求。',
    panelTitle: '项目级权限与合规操作',
    panelNote: '所有读写均绑定已授权项目与环境，关键操作自动落审计记录。',
    rows: [
      {
        label: '成员与邀请',
        value: '角色权限 / 一次性邀请链',
        detail: '支持自助注册或已有账号加入',
      },
      { label: '机器访问', value: '受限范围 Bearer 凭据', detail: '支持一次性密钥轮换与即时撤销' },
      { label: '生命周期', value: '合规删除 / 保留 / 归档', detail: '支持异步导出与历史回补' },
    ],
  },
];

export function LandingPage() {
  const [scrolled, setScrolled] = useState(false);
  const [activeId, setActiveId] = useState(CAPABILITIES[0]!.id);

  useEffect(() => {
    const onScroll = () => setScrolled(window.scrollY > 40);
    onScroll();
    window.addEventListener('scroll', onScroll, { passive: true });
    return () => window.removeEventListener('scroll', onScroll);
  }, []);

  const active = CAPABILITIES.find((item) => item.id === activeId) ?? CAPABILITIES[0]!;

  return (
    <div className="evia-landing">
      <header
        className={`evia-landing-header${scrolled ? ' evia-landing-header--solid' : ''}`}
        aria-label="主导航"
      >
        <div className="evia-landing-header-inner">
          <Link to="/" className="evia-landing-brand" aria-label="Evia 映途">
            <img src="/evia-icon.png" alt="" width={24} height={24} />
            <span>
              Evia <span className="evia-landing-brand-sub">映途</span>
            </span>
          </Link>
          <nav className="evia-landing-nav" aria-label="页面导航">
            <a href="#capabilities">核心能力</a>
            <a href="#architecture">私有架构</a>
            <a href="#get-started">开始使用</a>
          </nav>
          <div className="evia-landing-actions">
            <Link to="/login" className="evia-landing-login-link">
              登录
            </Link>
            <Link to="/login" className="evia-landing-primary-btn">
              进入控制台
            </Link>
          </div>
        </div>
      </header>

      <main>
        <section className="evia-landing-hero">
          <div className="evia-landing-hero-media" aria-hidden="true">
            <img
              className="evia-landing-hero-poster"
              src="/media/hero-horizon-poster.jpg"
              alt=""
              loading="eager"
              decoding="async"
            />
            <video
              className="evia-landing-hero-video"
              src="/media/hero-horizon.mp4"
              poster="/media/hero-horizon-poster.jpg"
              autoPlay
              muted
              loop
              playsInline
              preload="auto"
            />
            <div className="evia-landing-hero-scrim" />
          </div>

          <div className="evia-landing-hero-content">
            <h1>
              <span>洞见每一次交互</span>
              <span className="evia-landing-hero-comma" aria-hidden="true">
                ，
              </span>
              <span>驱动确定性增长</span>
            </h1>
            <p className="evia-landing-lead">
              从全端事件采集、漏斗留存分析到加密会话回放与合规治理，完整部署在你自己的基础设施之上。
            </p>
            <div className="evia-landing-cta">
              <Link to="/login" className="evia-landing-primary-btn">
                登录控制台
              </Link>
              <a href="#capabilities" className="evia-landing-secondary-btn">
                了解能力
              </a>
            </div>
          </div>

          <p className="evia-landing-hero-note">
            Evia 映途为自托管产品分析平台，所有事件、身份与回放数据均留存于你的自有集群。
          </p>
        </section>

        <section id="capabilities" className="evia-dark-section">
          <div className="evia-container evia-cap-grid">
            <div className="evia-cap-left">
              <h2>
                一切产品洞察，
                <br />
                尽在一处。
              </h2>
              <div className="evia-accordion" role="tablist" aria-label="核心能力切换">
                {CAPABILITIES.map((item) => {
                  const isActive = item.id === active.id;
                  return (
                    <div
                      key={item.id}
                      className={`evia-accordion-item${isActive ? ' evia-accordion-item--active' : ''}`}
                    >
                      <button
                        type="button"
                        role="tab"
                        aria-selected={isActive}
                        className="evia-accordion-trigger"
                        onClick={() => setActiveId(item.id)}
                      >
                        <span className="evia-accordion-dot" aria-hidden="true" />
                        <span>{item.title}</span>
                      </button>
                      {isActive ? <p className="evia-accordion-body">{item.summary}</p> : null}
                    </div>
                  );
                })}
              </div>
              <Link to="/login" className="evia-text-link">
                进入控制台
              </Link>
            </div>

            <div className="evia-cap-panel" role="tabpanel" aria-label={active.title}>
              <div className="evia-cap-panel-head">
                <h3>{active.panelTitle}</h3>
                <p>{active.panelNote}</p>
              </div>
              {active.code ? (
                <pre className="evia-code-surface">{active.code}</pre>
              ) : (
                <dl className="evia-spec-list">
                  {active.rows?.map((row) => (
                    <div key={row.label} className="evia-spec-row">
                      <dt>{row.label}</dt>
                      <dd>
                        <strong>{row.value}</strong>
                        <span>{row.detail}</span>
                      </dd>
                    </div>
                  ))}
                </dl>
              )}
            </div>
          </div>
        </section>

        <section id="architecture" className="evia-light-section">
          <div className="evia-container">
            <div className="evia-light-head">
              <h2>数据只留在你的集群里</h2>
              <p>解耦的私有化数据流水线，兼顾流式事件摄入、列式多维分析与合规留存。</p>
            </div>
            <div className="evia-pillars">
              <div className="evia-pillar">
                <strong>100%</strong>
                <h3>数据主权完全自主</h3>
                <p>事件明细、用户身份与会话内容全部落在自有存储中，不经任何第三方中转。</p>
              </div>
              <div className="evia-pillar">
                <strong>全链路</strong>
                <h3>流式摄入与列式分析</h3>
                <p>Kafka 削峰解耦、ClickHouse 支撑多维聚合，Redis 提供分布式准入配额控制。</p>
              </div>
              <div className="evia-pillar">
                <strong>多环境</strong>
                <h3>隔离开发与生产发布</h3>
                <p>原生隔离开发、预发与生产环境，Ed25519 配置签名校验保障客户端策略可信下发。</p>
              </div>
            </div>
            <p className="evia-stack-line">
              PostgreSQL 元数据与治理 | ClickHouse 列式引擎 | Kafka 事件总线 | Redis 实时准入 |
              Ed25519 配置签名
            </p>
          </div>
        </section>

        <section id="get-started" className="evia-closing-section">
          <div className="evia-container evia-closing-inner">
            <h2>更清晰地理解你的产品</h2>
            <p>使用平台管理员分配的账号登录 Evia 控制台，创建项目并接入首个事件源。</p>
            <Link to="/login" className="evia-landing-primary-btn">
              登录控制台
            </Link>
          </div>
        </section>
      </main>

      <footer className="evia-landing-footer">
        <div className="evia-container evia-footer-inner">
          <div className="evia-footer-brand">
            <img src="/evia-icon.png" alt="" width={20} height={20} />
            <span>Evia 映途</span>
          </div>
          <span>自托管事件与产品分析平台</span>
          <Link to="/login">控制台登录</Link>
        </div>
      </footer>
    </div>
  );
}
