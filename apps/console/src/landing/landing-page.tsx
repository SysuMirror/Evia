import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import './landing-page.css';

const FUNNEL_STEPS = [
  { label: '访问落地页 · page_view', count: '48,210', pct: '100%' },
  { label: '触发核心交互 · feature_try', count: '26,997', pct: '56%' },
  { label: '完成关键转化 · order_paid', count: '14,849', pct: '31%' },
];

export function LandingPage() {
  const [scrolled, setScrolled] = useState(false);

  useEffect(() => {
    const onScroll = () => setScrolled(window.scrollY > 24);
    onScroll();
    window.addEventListener('scroll', onScroll, { passive: true });
    return () => window.removeEventListener('scroll', onScroll);
  }, []);

  return (
    <div className="evia-landing">
      <div className="evia-landing-nav-wrap">
        <header
          className={`evia-landing-nav${scrolled ? ' evia-landing-nav--scrolled' : ''}`}
          aria-label="主导航"
        >
          <Link to="/" className="evia-landing-brand" aria-label="Evia 映途">
            <img src="/evia-icon.png" alt="" width={26} height={26} />
            <span>
              Evia <span className="evia-landing-brand-sub">映途</span>
            </span>
          </Link>
          <nav className="evia-landing-links" aria-label="页面导航">
            <a href="#capabilities">核心能力</a>
            <a href="#architecture">自托管架构</a>
            <a href="#get-started">开始使用</a>
          </nav>
          <div className="evia-landing-actions">
            <Link to="/login" className="evia-landing-ghost-btn">
              登录
            </Link>
            <Link to="/login" className="evia-landing-primary-btn">
              进入控制台
            </Link>
          </div>
        </header>
      </div>

      <main>
        <section className="evia-landing-hero">
          <div className="evia-landing-hero-backdrop" aria-hidden="true">
            <svg
              className="evia-landing-horizon-svg"
              viewBox="0 0 1440 900"
              fill="none"
              preserveAspectRatio="xMidYMid slice"
            >
              <defs>
                <radialGradient id="evia-hero-sun" cx="52%" cy="66%" r="54%">
                  <stop offset="0%" stopColor="#f0c8b0" stopOpacity="0.46" />
                  <stop offset="38%" stopColor="#8596c7" stopOpacity="0.24" />
                  <stop offset="100%" stopColor="#182034" stopOpacity="0" />
                </radialGradient>
                <radialGradient id="evia-hero-sky-glow" cx="82%" cy="14%" r="44%">
                  <stop offset="0%" stopColor="#7286ff" stopOpacity="0.28" />
                  <stop offset="100%" stopColor="#182034" stopOpacity="0" />
                </radialGradient>
                <linearGradient id="evia-ridge-far" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="#4c5c80" stopOpacity="0.52" />
                  <stop offset="100%" stopColor="#26324d" stopOpacity="0.86" />
                </linearGradient>
                <linearGradient id="evia-ridge-mid" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="#2c3856" stopOpacity="0.82" />
                  <stop offset="100%" stopColor="#171f33" stopOpacity="0.96" />
                </linearGradient>
                <linearGradient id="evia-ridge-near" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="#192136" stopOpacity="0.95" />
                  <stop offset="100%" stopColor="#0f1525" stopOpacity="1" />
                </linearGradient>
              </defs>
              <rect width="1440" height="900" fill="url(#evia-hero-sun)" />
              <rect width="1440" height="900" fill="url(#evia-hero-sky-glow)" />
              <path
                d="M0 570 C 190 500, 340 530, 520 492 C 700 455, 860 520, 1080 468 C 1230 432, 1340 468, 1440 442 L 1440 900 L 0 900 Z"
                fill="url(#evia-ridge-far)"
              />
              <path
                d="M0 645 C 210 568, 410 642, 650 582 C 880 524, 1110 618, 1440 530 L 1440 900 L 0 900 Z"
                fill="url(#evia-ridge-mid)"
              />
              <path
                d="M0 725 C 260 668, 520 744, 790 688 C 1030 638, 1250 706, 1440 662 L 1440 900 L 0 900 Z"
                fill="url(#evia-ridge-near)"
              />
            </svg>
          </div>

          <div className="evia-landing-hero-inner">
            <div className="evia-landing-eyebrow">
              <span className="evia-landing-eyebrow-dot" aria-hidden="true" />
              <span>自托管 · 实时事件与产品分析平台</span>
            </div>
            <h1>
              <span>洞见每一次交互</span>
              <span>驱动确定性增长</span>
            </h1>
            <p className="evia-landing-lead">
              从全端埋点采集、漏斗留存分析到加密会话回放与合规数据治理，全链路部署在你自己的基础设施之上。
            </p>
            <div className="evia-landing-cta-bar">
              <Link to="/login" className="evia-landing-primary-btn">
                登录控制台
              </Link>
              <a href="#capabilities" className="evia-landing-secondary-btn">
                浏览核心能力
              </a>
            </div>
          </div>

          <div className="evia-landing-stage" aria-label="Evia 分析概览预览">
            <div className="evia-stage-topbar">
              <div className="evia-stage-dots" aria-hidden="true">
                <span />
                <span />
                <span />
              </div>
              <span className="evia-stage-title">Evia 实时工作台 · 生产环境</span>
              <span className="evia-stage-env">在线 · 0 数据出境</span>
            </div>
            <div className="evia-stage-grid">
              <div className="evia-stage-panel">
                <div className="evia-stage-kpis">
                  <div className="evia-stage-kpi">
                    <span>今日活跃用户</span>
                    <strong>
                      32,480<em>+14.2%</em>
                    </strong>
                  </div>
                  <div className="evia-stage-kpi">
                    <span>实时事件写入</span>
                    <strong>
                      1.84M<em>+21.6%</em>
                    </strong>
                  </div>
                  <div className="evia-stage-kpi">
                    <span>核心转化率</span>
                    <strong>
                      30.8%<em>+3.4%</em>
                    </strong>
                  </div>
                </div>
                <svg className="evia-stage-chart" viewBox="0 0 520 118" fill="none">
                  <defs>
                    <linearGradient id="evia-stage-area" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor="#7c8dff" stopOpacity="0.38" />
                      <stop offset="100%" stopColor="#7c8dff" stopOpacity="0.02" />
                    </linearGradient>
                  </defs>
                  <g stroke="#ffffff" strokeOpacity="0.08" strokeDasharray="3 5">
                    <line x1="0" y1="24" x2="520" y2="24" />
                    <line x1="0" y1="62" x2="520" y2="62" />
                    <line x1="0" y1="100" x2="520" y2="100" />
                  </g>
                  <path
                    d="M6 94 C 52 84, 78 66, 126 70 C 174 74, 206 42, 258 46 C 310 50, 344 26, 396 30 C 446 34, 480 14, 514 10 L 514 114 L 6 114 Z"
                    fill="url(#evia-stage-area)"
                  />
                  <path
                    d="M6 94 C 52 84, 78 66, 126 70 C 174 74, 206 42, 258 46 C 310 50, 344 26, 396 30 C 446 34, 480 14, 514 10"
                    stroke="#a4b1ff"
                    strokeWidth="2.4"
                    strokeLinecap="round"
                  />
                  <circle cx="514" cy="10" r="4.5" fill="#ffffff" />
                </svg>
              </div>
              <div className="evia-stage-panel">
                <div className="evia-stage-funnel-title">核心转化漏斗（近 7 天）</div>
                <div className="evia-stage-funnel">
                  {FUNNEL_STEPS.map((step) => (
                    <div key={step.label}>
                      <div className="evia-stage-step-head">
                        <span>{step.label}</span>
                        <strong>
                          {step.count} · {step.pct}
                        </strong>
                      </div>
                      <div className="evia-stage-bar">
                        <span style={{ width: step.pct }} />
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          </div>
        </section>

        <section id="capabilities" className="evia-landing-section">
          <div className="evia-section-head">
            <span className="evia-section-kicker">核心能力</span>
            <h2>一套工作台，打通采集、分析与合规治理</h2>
            <p>
              不需要在多套割裂的埋点工具之间来回切换。Evia
              将事件契约、实时查询、看板报告与隐私治理统一到同一数据底座。
            </p>
          </div>
          <div className="evia-bento-grid">
            <article className="evia-bento-card">
              <div>
                <span className="evia-bento-tag">洞察分析</span>
                <h3>多维事件、漏斗、留存与公式指标</h3>
                <p>
                  支持自定义细分、转化窗口、留存队列、视口点击热力图与复合公式指标，常用分析一键保存并编排到自定义看板与报告。
                </p>
              </div>
              <div className="evia-bento-preview">
                <div className="evia-preview-chips">
                  <span>事件趋势</span>
                  <span>转化漏斗</span>
                  <span>留存队列</span>
                  <span>点击热力图</span>
                  <span>复合公式指标</span>
                  <span>自定义看板</span>
                </div>
              </div>
            </article>

            <article className="evia-bento-card">
              <div>
                <span className="evia-bento-tag">契约接入</span>
                <h3>强类型事件定义与实时采集诊断</h3>
                <p>
                  管理开发、预发与生产多环境事件版本，自动生成 TypeScript
                  埋点产物，实时监控写入吞吐、限流与死信异常。
                </p>
              </div>
              <div className="evia-bento-preview">
                <pre className="evia-preview-code">{`evia.emit('checkout_completed', {
  plan: 'pro',
  amount_cents: 19900,
  currency: 'CNY',
});`}</pre>
              </div>
            </article>

            <article className="evia-bento-card">
              <div>
                <span className="evia-bento-tag">行为还原</span>
                <h3>端到端加密会话回放与身份修订</h3>
                <p>
                  结合 RSA
                  公私钥加密隔离敏感回放内容，支持匿名访客与登录账号的跨端身份归并，把冷冰冰的数字还原为真实体验。
                </p>
              </div>
              <div className="evia-bento-preview">
                <div className="evia-preview-chips">
                  <span>RSA-2048 内容加密</span>
                  <span>身份图谱修订</span>
                  <span>交互时间轴</span>
                </div>
              </div>
            </article>

            <article className="evia-bento-card">
              <div>
                <span className="evia-bento-tag">数据治理</span>
                <h3>细粒度权限、删除擦除与归档回补</h3>
                <p>
                  内置项目角色治理与邀请协作、机器凭证管理、合规数据删除、可验证归档保留与历史回补流水线，满足团队长期演进要求。
                </p>
              </div>
              <div className="evia-bento-preview">
                <div className="evia-preview-chips">
                  <span>项目成员邀请</span>
                  <span>合规数据擦除</span>
                  <span>保留策略与归档</span>
                  <span>异步数据导出</span>
                </div>
              </div>
            </article>
          </div>
        </section>

        <section id="architecture" className="evia-landing-section">
          <div className="evia-section-head">
            <span className="evia-section-kicker">自托管架构</span>
            <h2>数据只留在你的集群里</h2>
            <p>面向私有化部署设计的解耦流水线，兼顾异步流式摄入与多维列式分析。</p>
          </div>
          <div className="evia-arch-strip">
            <div className="evia-arch-item">
              <strong>100%</strong>
              <h3>数据主权完全自主</h3>
              <p>所有事件明细、用户身份与会话内容均保存在自有存储，无任何第三方回传。</p>
            </div>
            <div className="evia-arch-item">
              <strong>全链路</strong>
              <h3>流式摄入与列式分析</h3>
              <p>Kafka 削峰解耦、ClickHouse 支撑列式多维聚合，Redis 护航实时限流准入。</p>
            </div>
            <div className="evia-arch-item">
              <strong>多环境</strong>
              <h3>隔离开发与生产发布</h3>
              <p>原生区分开发、预发与生产环境，配置签名校验确保客户端策略可信下发。</p>
            </div>
          </div>
          <div className="evia-stack-row" aria-label="核心技术底座">
            <span>PostgreSQL 元数据与治理</span>
            <span>ClickHouse 列式分析引擎</span>
            <span>Kafka 异步事件总线</span>
            <span>Redis 实时准入限流</span>
            <span>Ed25519 配置签名</span>
          </div>
        </section>

        <section id="get-started" className="evia-landing-section">
          <div className="evia-closing-band">
            <h2>准备好更清晰地理解你的产品了吗？</h2>
            <p>使用平台管理员分配的账号登录 Evia 控制台，立即创建项目并接入首个事件源。</p>
            <Link to="/login" className="evia-landing-primary-btn">
              立即登录控制台
            </Link>
          </div>
        </section>
      </main>

      <footer className="evia-landing-footer">
        <div className="evia-landing-footer-brand">
          <img src="/evia-icon.png" alt="" width={22} height={22} />
          <span>Evia 映途</span>
        </div>
        <span>自托管事件与产品分析平台</span>
        <Link to="/login">控制台登录 →</Link>
      </footer>
    </div>
  );
}
