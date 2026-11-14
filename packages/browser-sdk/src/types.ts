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
