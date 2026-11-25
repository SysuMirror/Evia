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
    tag: '产品洞察',
    title: '事件、漏斗与留存分析',
    summary:
      '通过趋势统计、有序转化漏斗、留存队列与复合公式指标看清产品体验瓶颈；分析口径版本化固化，只围绕必要业务信号展开洞察。',
    panelTitle: '克制而清晰的产品分析',
    panelNote: '查询任务与版本化分析口径保持一致，避免团队间口径漂移与冗余追踪。',
    rows: [
      { label: '事件与趋势', value: '计数 / 去重 UV / 属性分组', detail: '支持按环境与时区聚合分析' },
      { label: '有序漏斗', value: '多步转化窗口', detail: '逐步骤展示留存与流失比例' },
      { label: '留存与路径', value: '队列留存 / 关键路径', detail: '基于结构化交互信号定位体验断点' },
      { label: '指标与看板', value: '独立指标 / 复合公式', detail: '多图固定版本统一运行与复用' },
    ],
  },
  {
    id: 'privacy',
    tag: '隐私保护',
    title: '默认隐私保护与端到端加密',
    summary:
      '敏感内容在离开浏览器前即完成客户端脱敏与 RSA-OAEP 混合加密，默认尊重用户的退出分析偏好，私钥仅由授权主体掌握。',
    panelTitle: '零明文暴露的隐私边界',
    panelNote: '客户端先脱敏、再加密，服务端与传输链路均无法擅自读取明文内容。',
    rows: [
      {
        label: '客户端加密',
        value: 'AES-256-GCM + RSA-OAEP',
        detail: '敏感载荷在浏览器端封装，私钥不进入主链路',
      },
      {
        label: '脱敏与选择权',
        value: '输入自动掩码 / 一键退出',
        detail: '密码、令牌与表单输入默认遮蔽，随时尊重用户退出偏好',
      },
      {
        label: '身份边界',
        value: '匿名隔离与可审计修订',
        detail: '匿名与登录态边界清晰，历史修订可追溯且可彻底擦除',
      },
    ],
  },
  {
    id: 'contracts',
    tag: '数据契约',
    title: '强类型契约与多环境发布',
    summary:
      '以显式白名单契约严格约束允许记录的字段结构，自动拦截未声明属性；区分开发、预发与生产环境不可变发布。',
    panelTitle: '白名单契约与最小化边界',
    panelNote: '零运行时依赖，自动校验 Ed25519 签名配置，杜绝未授权字段越界写入。',
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
    id: 'governance',
    tag: '权限合规',
    title: '细粒度治理与数据生命周期',
    summary:
      '提供项目级最小权限角色、一键合规删除、自动到期清理与不可篡改审计日志，让每一份数据的生命周期清晰可控。',
    panelTitle: '全生命周期合规与审计',
    panelNote: '所有读写与敏感解密均绑定已授权项目与环境，关键操作自动落审计记录。',
    rows: [
      {
        label: '最小权限与邀请',
        value: '角色权限 / 一次性邀请链',
        detail: '按项目与环境隔离访问范围，防止越权查看',
      },
      {
        label: '删除与保留',
        value: '一键合规擦除 / 到期清理',
        detail: '快速响应主体删除请求，超期数据按策略自动清除',
      },
      {
        label: '机器凭据',
        value: '受限作用域 Bearer 密钥',
        detail: '支持一次性签发、密钥轮换与即时撤销',
      },
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
    <div id="top" ref={rootRef} className="evia-landing">
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
              隐私与安全
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
              以隐私保护为先的自托管产品分析平台，通过端到端加密、最小化数据边界与合规治理，在守护用户隐私的前提下理解产品演进。
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
            默认尊重用户隐私偏好，支持随时退出分析；所有数据仅留存于你的自有集群，永不流向第三方。
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
              <h2>隐私不是附加项，而是默认前提</h2>
              <p>从浏览器端默认脱敏加密到自有集群独立落盘，每一层设计都围绕「最小化、可验证、可删除」构建。</p>
            </div>
            <div className="evia-pillars">
              <div className="evia-pillar" data-reveal="pillar" data-delay="0">
                <strong>零外流</strong>
                <h3>100% 自有集群数据主权</h3>
                <p>所有分析结果、身份边界与审计日志全部留在你掌控的基础设施中，绝不经任何第三方云中转或共享。</p>
              </div>
              <div className="evia-pillar" data-reveal="pillar" data-delay="1">
                <strong>端到端</strong>
                <h3>客户端默认脱敏与加密</h3>
                <p>表单输入、令牌与敏感区域默认自动掩码，加密载荷在离开浏览器前即完成封装，未经授权无法解密。</p>
              </div>
              <div className="evia-pillar" data-reveal="pillar" data-delay="2">
                <strong>可撤回</strong>
                <h3>尊重选择与全周期合规删除</h3>
                <p>原生支持用户一键退出分析，并内建按主体彻底擦除、数据保留期自动清理与全量操作审计追踪。</p>
              </div>
            </div>
          </div>
        </section>

        <section ref={closingRef} id="get-started" className="evia-closing-section">
          <div className="evia-container">
            <div className="evia-closing-inner" data-reveal="up">
              <h2>守护用户隐私，更清晰地理解你的产品</h2>
              <p>使用平台管理员分配的账号登录 Evia 映途控制台，在自有基础设施中开启安全可控的产品洞察。</p>
              <div className="evia-closing-actions">
                <Link to="/login" className="evia-landing-primary-btn">
                  登录控制台
                </Link>
                <a href="#architecture" className="evia-text-link">
                  了解隐私保护
                </a>
              </div>
            </div>
          </div>
        </section>
      </main>

      <footer className="evia-landing-footer">
        <div className="evia-container">
          <div className="evia-footer-grid">
            <div className="evia-footer-col evia-footer-col--brand">
              <Link to="/" className="evia-footer-brand" aria-label="Evia 映途">
                <img src="/evia-icon-white.png" alt="" width={22} height={22} />
                <span>
                  EVIA <span className="evia-footer-brand-sub">映途</span>
                </span>
              </Link>
              <p className="evia-footer-desc">
                以隐私保护为先的自托管产品分析平台，坚持最小化边界、端到端加密与自有集群存储。
              </p>
            </div>

            <div className="evia-footer-col">
              <h4>核心能力</h4>
              <ul>
                <li>
                  <a href="#capabilities" onClick={() => setActiveId('analytics')}>
                    事件、漏斗与留存
                  </a>
                </li>
                <li>
                  <a href="#capabilities" onClick={() => setActiveId('privacy')}>
                    隐私保护与端到端加密
                  </a>
                </li>
                <li>
                  <a href="#capabilities" onClick={() => setActiveId('contracts')}>
                    白名单契约与多环境发布
                  </a>
                </li>
                <li>
                  <a href="#capabilities" onClick={() => setActiveId('governance')}>
                    权限治理与数据生命周期
                  </a>
                </li>
              </ul>
            </div>

            <div className="evia-footer-col">
              <h4>隐私承诺</h4>
              <ul>
                <li>
                  <a href="#architecture">自有集群数据主权</a>
                </li>
                <li>
                  <a href="#architecture">敏感信息默认脱敏</a>
                </li>
                <li>
                  <a href="#architecture">尊重用户退出偏好</a>
                </li>
                <li>
                  <a href="#architecture">合规擦除与保留策略</a>
                </li>
              </ul>
            </div>

            <div className="evia-footer-col">
              <h4>账号与入口</h4>
              <ul>
                <li>
                  <Link to="/login">控制台登录</Link>
                </li>
                <li>
                  <a href="#get-started">开始使用</a>
                </li>
                <li>
                  <a href="#top">返回顶部</a>
                </li>
              </ul>
            </div>
          </div>

          <div className="evia-footer-bottom">
            <p className="evia-footer-copyright">
              © {new Date().getFullYear()} Evia 映途. All rights reserved.
            </p>
            <p className="evia-footer-meta">
              Evia 映途为自托管产品分析平台，所有分析数据、身份边界与审计日志均留存于部署方自有基础设施，不经第三方云中转。
            </p>
          </div>
        </div>
      </footer>
    </div>
  );
}
