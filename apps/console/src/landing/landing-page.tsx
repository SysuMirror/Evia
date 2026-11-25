import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import './landing-page.css';

interface CapabilityItem {
  id: string;
  tag: string;
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
    tag: '分析引擎',
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
    tag: '事件契约',
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
    tag: '隐私回放',
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
    tag: '权限合规',
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

type HeaderMode = 'transparent' | 'solid-dark' | 'solid-light';
type SectionId = 'capabilities' | 'architecture' | 'get-started' | '';

function clamp01(value: number) {
  return Math.max(0, Math.min(1, value));
}

export function LandingPage() {
  const rootRef = useRef<HTMLDivElement>(null);
  const heroRef = useRef<HTMLElement>(null);
  const capRef = useRef<HTMLElement>(null);
  const archRef = useRef<HTMLElement>(null);
  const closingRef = useRef<HTMLElement>(null);

  const [headerMode, setHeaderMode] = useState<HeaderMode>('transparent');
  const [activeSection, setActiveSection] = useState<SectionId>('');
  const [activeId, setActiveId] = useState(CAPABILITIES[0]!.id);

  useEffect(() => {
    const root = rootRef.current;
    if (!root) return;

    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    const revealTargets = Array.from(root.querySelectorAll<HTMLElement>('[data-reveal]'));

    if (reducedMotion || typeof IntersectionObserver === 'undefined') {
      revealTargets.forEach((el) => {
        el.dataset.revealed = 'true';
      });
    } else {
      const observer = new IntersectionObserver(
        (entries) => {
          entries.forEach((entry) => {
            if (entry.isIntersecting) {
              (entry.target as HTMLElement).dataset.revealed = 'true';
              observer.unobserve(entry.target);
            }
          });
        },
        { threshold: 0.14, rootMargin: '0px 0px -8% 0px' },
      );
      revealTargets.forEach((el) => observer.observe(el));

      return () => observer.disconnect();
    }
  }, []);

  useEffect(() => {
    const root = rootRef.current;
    if (!root) return;

    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    let rafId = 0;

    const update = () => {
      rafId = 0;
      const scrollY = window.scrollY;
      const vh = window.innerHeight || 1;
      const maxScroll = Math.max(1, document.documentElement.scrollHeight - vh);
      const pageProgress = clamp01(scrollY / maxScroll);
      root.style.setProperty('--page-progress', pageProgress.toFixed(4));

      const heroRect = heroRef.current?.getBoundingClientRect();
      const heroHeight = heroRect?.height || vh;
      const heroProgress = reducedMotion ? 0 : clamp01(scrollY / Math.max(1, heroHeight * 0.85));
      root.style.setProperty('--hero-progress', heroProgress.toFixed(4));

      if (capRef.current) {
        const r = capRef.current.getBoundingClientRect();
        const capProgress = reducedMotion
          ? 0.5
          : clamp01((vh - r.top) / Math.max(1, vh + r.height * 0.65));
        root.style.setProperty('--cap-progress', capProgress.toFixed(4));
      }

      if (closingRef.current) {
        const r = closingRef.current.getBoundingClientRect();
        const closingProgress = reducedMotion
          ? 1
          : clamp01((vh - r.top) / Math.max(1, vh * 0.78));
        root.style.setProperty('--closing-progress', closingProgress.toFixed(4));
      }

      const archRect = archRef.current?.getBoundingClientRect();
      const nextMode: HeaderMode =
        scrollY <= 40
          ? 'transparent'
          : archRect && archRect.top <= 72 && archRect.bottom > 36
            ? 'solid-light'
            : 'solid-dark';
      setHeaderMode((prev) => (prev === nextMode ? prev : nextMode));

      const probeY = vh * 0.38;
      let nextSection: SectionId = '';
      if (closingRef.current && closingRef.current.getBoundingClientRect().top <= probeY) {
        nextSection = 'get-started';
      } else if (archRef.current && archRef.current.getBoundingClientRect().top <= probeY) {
        nextSection = 'architecture';
      } else if (capRef.current && capRef.current.getBoundingClientRect().top <= probeY) {
        nextSection = 'capabilities';
      }
      setActiveSection((prev) => (prev === nextSection ? prev : nextSection));
    };

    const schedule = () => {
      if (!rafId) rafId = window.requestAnimationFrame(update);
    };

    update();
    window.addEventListener('scroll', schedule, { passive: true });
    window.addEventListener('resize', schedule, { passive: true });
    return () => {
      if (rafId) window.cancelAnimationFrame(rafId);
      window.removeEventListener('scroll', schedule);
      window.removeEventListener('resize', schedule);
    };
  }, []);

  const active = CAPABILITIES.find((item) => item.id === activeId) ?? CAPABILITIES[0]!;
  const isLightHeader = headerMode === 'solid-light';
  const headerClass =
    headerMode === 'transparent'
      ? 'evia-landing-header'
      : headerMode === 'solid-light'
        ? 'evia-landing-header evia-landing-header--solid evia-landing-header--light'
        : 'evia-landing-header evia-landing-header--solid';

  return (
    <div ref={rootRef} className="evia-landing">
      <div className="evia-scroll-progress" aria-hidden="true" />

      <header className={headerClass} aria-label="主导航">
        <div className="evia-landing-header-inner">
          <Link to="/" className="evia-landing-brand" aria-label="Evia 映途">
            <span className="evia-brand-mark" aria-hidden="true">
              <img
                src="/evia-icon-white.png"
                alt=""
                width={24}
                height={24}
                className={`evia-brand-img evia-brand-img--white${isLightHeader ? ' is-hidden' : ''}`}
              />
              <img
                src="/evia-icon.png"
                alt=""
                width={24}
                height={24}
                className={`evia-brand-img evia-brand-img--color${isLightHeader ? '' : ' is-hidden'}`}
              />
            </span>
            <span className="evia-landing-brand-text">
              EVIA <span className="evia-landing-brand-sub">映途</span>
            </span>
          </Link>

          <nav className="evia-landing-nav" aria-label="页面导航">
            <a
              href="#capabilities"
              className={activeSection === 'capabilities' ? 'is-active' : undefined}
            >
              核心能力
            </a>
            <a
              href="#architecture"
              className={activeSection === 'architecture' ? 'is-active' : undefined}
            >
              私有架构
            </a>
            <a
              href="#get-started"
              className={activeSection === 'get-started' ? 'is-active' : undefined}
            >
              开始使用
            </a>
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
        <section ref={heroRef} className="evia-landing-hero">
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
            <h1 className="evia-hero-enter evia-hero-enter--1">
              <span>洞见每一次交互</span>
              <span className="evia-landing-hero-comma" aria-hidden="true">
                ，
              </span>
              <span>驱动确定性增长</span>
            </h1>
            <p className="evia-landing-lead evia-hero-enter evia-hero-enter--2">
              从全端事件采集、漏斗留存分析到加密会话回放与合规治理，完整部署在你自己的基础设施之上。
            </p>
            <div className="evia-landing-cta evia-hero-enter evia-hero-enter--3">
              <Link to="/login" className="evia-landing-primary-btn">
                登录控制台
              </Link>
              <a href="#capabilities" className="evia-landing-secondary-btn">
                了解能力
              </a>
            </div>
          </div>

          <p className="evia-landing-hero-note evia-hero-enter evia-hero-enter--4">
            Evia 映途为自托管产品分析平台，所有事件、身份与回放数据均留存于你的自有集群。
          </p>
        </section>

        <section ref={capRef} id="capabilities" className="evia-dark-section">
          <div className="evia-container evia-cap-grid">
            <div className="evia-cap-left">
              <h2 data-reveal="up">
                一切产品洞察，
                <br />
                尽在一处。
              </h2>
              <div
                className="evia-accordion"
                role="tablist"
                aria-label="核心能力切换"
                data-reveal="up"
                data-delay="1"
              >
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
                      <div className="evia-accordion-collapse" aria-hidden={!isActive}>
                        <div className="evia-accordion-collapse-inner">
                          <p className="evia-accordion-body">{item.summary}</p>
                        </div>
                      </div>
                    </div>
                  );
                })}
              </div>
              <div data-reveal="up" data-delay="2">
                <Link to="/login" className="evia-text-link">
                  进入控制台
                </Link>
              </div>
            </div>

            <div className="evia-cap-stage" data-reveal="panel" data-delay="1">
              <div
                key={active.id}
                className="evia-cap-panel"
                role="tabpanel"
                aria-label={active.title}
              >
                <div className="evia-cap-panel-head">
                  <span className="evia-cap-panel-tag">{active.tag}</span>
                  <h3>{active.panelTitle}</h3>
                  <p>{active.panelNote}</p>
                </div>
                {active.code ? (
                  <pre className="evia-code-surface">{active.code}</pre>
                ) : (
                  <dl className="evia-spec-list">
                    {active.rows?.map((row, index) => (
                      <div
                        key={row.label}
                        className="evia-spec-row"
                        style={{ '--row-index': index } as React.CSSProperties}
                      >
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
          </div>
        </section>

        <section ref={archRef} id="architecture" className="evia-light-section">
          <div className="evia-container">
            <div className="evia-light-head" data-reveal="up">
              <h2>数据只留在你的集群里</h2>
              <p>解耦的私有化数据流水线，兼顾流式事件摄入、列式多维分析与合规留存。</p>
            </div>
            <div className="evia-pillars">
              <div className="evia-pillar" data-reveal="pillar" data-delay="0">
                <strong>100%</strong>
                <h3>数据主权完全自主</h3>
                <p>事件明细、用户身份与会话内容全部落在自有存储中，不经任何第三方中转。</p>
              </div>
              <div className="evia-pillar" data-reveal="pillar" data-delay="1">
                <strong>全链路</strong>
                <h3>流式摄入与列式分析</h3>
                <p>Kafka 削峰解耦、ClickHouse 支撑多维聚合，Redis 提供分布式准入配额控制。</p>
              </div>
              <div className="evia-pillar" data-reveal="pillar" data-delay="2">
                <strong>多环境</strong>
                <h3>隔离开发与生产发布</h3>
                <p>原生隔离开发、预发与生产环境，Ed25519 配置签名校验保障客户端策略可信下发。</p>
              </div>
            </div>
            <p className="evia-stack-line" data-reveal="up" data-delay="2">
              PostgreSQL 元数据与治理 | ClickHouse 列式引擎 | Kafka 事件总线 | Redis 实时准入 |
              Ed25519 配置签名
            </p>
          </div>
        </section>

        <section ref={closingRef} id="get-started" className="evia-closing-section">
          <div className="evia-closing-glow" aria-hidden="true" />
          <div className="evia-container evia-closing-inner" data-reveal="up">
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
            <img src="/evia-icon-white.png" alt="" width={20} height={20} />
            <span>Evia 映途</span>
          </div>
          <span>自托管事件与产品分析平台</span>
          <Link to="/login">控制台登录</Link>
        </div>
      </footer>
    </div>
  );
}
