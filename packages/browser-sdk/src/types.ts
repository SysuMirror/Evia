export type Environment = 'development' | 'staging' | 'production';
export type Scalar = string | number | boolean;
export type Properties = Readonly<Record<string, Scalar | readonly Scalar[]>>;

export interface PublicConfig {
  format: 'evia.public-config.v1';
  project_id: string;
  environment: Environment;
  release_revision: number;
  events: readonly {
    event_key: string;
    schema_version: number;
    schema: Readonly<Record<string, unknown>>;
  }[];
}

export interface StoragePort {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

/** Injectable browser scroll surface, also used for deterministic tests. */
export interface ScrollHost {
  /** Subscribe to scroll activity; returns an unsubscribe function. */
  subscribe(listener: () => void): () => void;
  /** Current vertical scroll position as a ratio in [0, 1]. */
  ratio(): number;
}

export interface Clock {
  now(): number;
  setTimeout(callback: () => void, delay: number): ReturnType<typeof setTimeout>;
  clearTimeout(timer: ReturnType<typeof setTimeout>): void;
}

export interface ClientOptions {
  endpoint: string;
  writeKey: string;
  projectId: string;
  environment: Environment;
  fetch?: typeof globalThis.fetch;
  /** null disables persistence; failures fall back to memory. */
  storage?: StoragePort | null;
  /**
   * Opt-in durable mirror of the bounded event queue onto the storage port, so
   * queued events survive a page reload or same-context navigation. Defaults to
   * false, which keeps the historical memory-only queue byte-identical.
   * Redelivery after reload stays at-least-once: rehydrated events keep their
   * original event_id and the collector deduplicates stable source/event IDs.
   */
  persistQueue?: boolean;
  /**
   * Opt-in cross-document session continuity. When enabled the session scope
   * (session id, tab id, sequence and last activity) is mirrored onto a
   * tab-scoped storage port, so a reload or hard navigation within the same
   * tab continues the same session instead of starting a new one. Closing the
   * tab or opening a new one still starts a fresh session, and reset()
   * discards the persisted scope. Defaults to false so the per-document
   * session scope stays byte-identical.
   */
  persistSession?: boolean;
  /**
   * Tab-scoped storage port used by persistSession; the browser sessionStorage
   * is used by default. Passing null disables the port, so enabling
   * persistSession without a usable port fails closed at construction instead
   * of silently losing continuity.
   */
  sessionStorage?: StoragePort | null;
  /** Injectable browser capabilities, also useful for deterministic tests. */
  clock?: Clock;
  randomUUID?: () => string;
  lifecycle?: boolean;
  /**
   * Opt-in automatic sys.page_view collection. When enabled the client emits one
   * reserved page-view event for the initial load and after every navigate()/reset()
   * that produces a new page_view_id. Defaults to false so existing behavior is unchanged.
   */
  autoPageViews?: boolean;
  /**
   * Opt-in automatic sys.identity_link collection. When enabled each successful
   * identify(uid) enqueues one reserved identity-link event (empty properties) whose
   * identity carries both the new uid and the anonymous id; no credentials are collected.
   * Defaults to false so existing behavior is unchanged.
   */
  autoIdentityLink?: boolean;
  /**
   * Opt-in automatic sys.session_end collection. When enabled the client records a
   * reserved session-end event before an observed session ends: logout on reset(),
   * account_switch when identify() changes the uid, and idle when the inactivity timeout
   * rotates the session. Defaults to false so existing behavior is unchanged.
   */
  autoSessionEnd?: boolean;
  /**
   * Opt-in automatic sys.page_scroll collection. Scroll activity is coalesced into one
   * event when scrolling stops, capped at 12 events per minute. Defaults to false.
   */
  autoPageScrolls?: boolean;
  /** Injectable scroll surface; a window/document adapter is used by default in a browser. */
  scrollHost?: ScrollHost;
  /** Debounce window used to decide that scrolling has stopped, in milliseconds. */
  scrollIdleMs?: number;
  configTtlMs?: number;
  requestTimeoutMs?: number;
  flushIntervalMs?: number;
  retryBaseMs?: number;
  maxAttempts?: number;
  maxAgeMs?: number;
  maxQueueEvents?: number;
  maxQueueBytes?: number;
  maxBatchEvents?: number;
  maxBatchBytes?: number;
  sessionTimeoutMs?: number;
}

export interface NavigationLink {
  readonly navigationId: string;
  readonly sourcePageViewId: string;
}

export interface PageOptions {
  routeName?: string;
  routeTemplate?: string;
  sourceModule?: string;
  navigationType?: 'initial' | 'push' | 'replace' | 'pop' | 'restore';
  link?: NavigationLink;
}

export interface EventOptions {
  operationId?: string;
  navigation?: NavigationLink;
}

export interface EventOutcome {
  eventId: string;
  /** accepted means a durable queue acknowledgement, not stored/queryable. */
  status: 'accepted' | 'rejected' | 'dropped' | 'uncertain';
  attempts: number;
  receiptId?: string;
  code?: string;
  retryable?: boolean;
}

export interface Emission {
  readonly eventId: string;
  readonly result: Promise<EventOutcome>;
}

export type OperationErrorCategory = 'validation' | 'network' | 'timeout' | 'http' | 'business';

/**
 * Structured, sanitized description of a terminal logical-operation failure.
 * Raw exceptions and request bodies are intentionally not accepted, so the SDK
 * never collects credentials or arbitrary payloads in sys.operation_failed.
 */
export interface OperationFailure {
  operation: string;
  errorCategory: OperationErrorCategory;
  retryable: boolean;
  errorCode?: string;
}

export type SessionEndReason = 'logout' | 'account_switch' | 'idle';

export interface FlushResult {
  pending: number;
}

export interface EventContext {
  session_id: string;
  tab_id: string;
  sequence: number;
  page_view_id: string;
  navigation_id: string;
  navigation_type: NonNullable<PageOptions['navigationType']>;
  source_page_view_id?: string;
  previous_page_view_id?: string;
  operation_id?: string;
  route_name?: string;
  route_template?: string;
  source_module?: string;
}

export interface Envelope {
  event_id: string;
  event_key: string;
  schema_version: number;
  release_revision: number;
  occurred_at: string;
  identity: { anonymous_id: string; uid?: string };
  context: EventContext;
  sdk: { name: 'evia-browser'; version: '0.1.0' };
  properties: Properties;
}

export interface BrowserClient {
  init(): Promise<PublicConfig>;
  refreshConfig(): Promise<PublicConfig>;
  emit(eventKey: string, properties?: Properties, options?: EventOptions): Emission;
  reportOperationFailure(failure: OperationFailure): Emission;
  identify(uid: string): void;
  reset(): void;
  beginNavigation(): NavigationLink;
  navigate(options?: PageOptions): Readonly<Omit<EventContext, 'sequence'>>;
  flush(options?: { keepalive?: boolean }): Promise<FlushResult>;
  disable(): void;
  enable(): Promise<PublicConfig>;
  destroy(): void;
  readonly pending: number;
}
