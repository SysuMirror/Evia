import type {
  BrowserClient,
  ClientOptions,
  Emission,
  Envelope,
  EventContext,
  EventOptions,
  EventOutcome,
  FlushResult,
  NavigationLink,
  OperationFailure,
  PageOptions,
  Properties,
  PublicConfig,
  StoragePort,
} from './types.js';
import {
  boundedInteger,
  byteLength,
  copyProperties,
  customEventPattern,
  EviaSDKError,
  requireText,
  uuidPattern,
  validateConfig,
  validateOptions,
} from './validation.js';
import { defaultClock, readJSON, retryAfter, Transport, validateReceipt } from './transport.js';

type Page = Omit<EventContext, 'sequence'>;
// pageViewEventKey is the reserved system event used by automatic page
// collection. It is not reachable through the public custom-event emit().
const pageViewEventKey = 'sys.page_view';
// identityLinkEventKey is the reserved system event recorded after a successful
// identify(); uid and anonymous id travel in identity, never in properties.
const identityLinkEventKey = 'sys.identity_link';
// operationFailedEventKey records one sanitized terminal logical-operation failure.
const operationFailedEventKey = 'sys.operation_failed';
const operationErrorCategories = new Set(['validation', 'network', 'timeout', 'http', 'business']);
interface Entry {
  eventId: string;
  draft: Omit<Envelope, 'schema_version' | 'release_revision'>;
  createdAt: number;
  bytes: number;
  json?: string;
  attempts: number;
  ambiguous: boolean;
  settle(outcome: EventOutcome): void;
}
interface Batch {
  id: string;
  body: string;
  entries: Entry[];
}

function storageDefault(): StoragePort | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage;
  } catch {
    return null;
  }
}
function freezeConfig(config: PublicConfig): PublicConfig {
  const pending: { value: object; depth: number }[] = [{ value: config, depth: 0 }];
  while (pending.length) {
    const { value, depth } = pending.pop()!;
    if (depth > 64) throw new EviaSDKError('CONFIG_TOO_DEEP');
    for (const child of Object.values(value))
      if (child !== null && typeof child === 'object')
        pending.push({ value: child, depth: depth + 1 });
    Object.freeze(value);
  }
  return config;
}

export function createEviaClient(options: ClientOptions): BrowserClient {
  return new Client(options);
}

class Client implements BrowserClient {
  private readonly endpoint: string;
  private readonly writeKey: string;
  private readonly projectId: string;
  private readonly environment: ClientOptions['environment'];
  private readonly clock;
  private readonly transport: Transport;
  private readonly uuid: () => string;
  private readonly storage: StoragePort | null;
  private readonly storageKey: string;
  private readonly configTtl: number;
  private readonly flushInterval: number;
  private readonly retryBase: number;
  private readonly maxAttempts: number;
  private readonly maxAge: number;
  private readonly maxQueueEvents: number;
  private readonly maxQueueBytes: number;
  private readonly maxBatchEvents: number;
  private readonly maxBatchBytes: number;
  private readonly sessionTimeout: number;
  private readonly autoPageViews: boolean;
  private readonly autoIdentityLink: boolean;
  private enabled = true;
  private destroyed = false;
  private generation = 0;
  private config?: PublicConfig;
  private configExpires = 0;
  private etag?: string;
  private configFlight?: Promise<PublicConfig>;
  private flushFlight?: Promise<FlushResult>;
  private timer?: ReturnType<typeof setTimeout>;
  private timerAt = Infinity;
  private nextAttempt = 0;
  private queue: Entry[] = [];
  private queueBytes = 0;
  private batch?: Batch;
  private uid?: string;
  private anonymousId: string;
  private readonly tabId: string;
  private sessionId: string;
  private lastActivity: number;
  private sequence = 0;
  private page: Page;
  private readonly cleanup: (() => void)[] = [];

  constructor(options: ClientOptions) {
    this.endpoint = validateOptions(options);
    this.writeKey = options.writeKey;
    this.projectId = options.projectId.toLowerCase();
    this.environment = options.environment;
    this.clock = options.clock ?? defaultClock;
    const fetcher = options.fetch ?? globalThis.fetch?.bind(globalThis);
    if (!fetcher) throw new EviaSDKError('FETCH_UNAVAILABLE');
    this.transport = new Transport(
      fetcher,
      this.clock,
      boundedInteger(options.requestTimeoutMs, 5000, 10, 30000),
    );
    this.uuid = () => {
      const id = options.randomUUID?.() ?? globalThis.crypto?.randomUUID();
      if (!id || !uuidPattern.test(id)) throw new EviaSDKError('SECURE_UUID_UNAVAILABLE');
      return id.toLowerCase();
    };
    this.configTtl = boundedInteger(options.configTtlMs, 30000, 1000, 60000);
    this.flushInterval = boundedInteger(options.flushIntervalMs, 1000, 10, 10000);
    this.retryBase = boundedInteger(options.retryBaseMs, 1000, 10, 60000);
    this.maxAttempts = boundedInteger(options.maxAttempts, 3, 1, 5);
    this.maxAge = boundedInteger(options.maxAgeMs, 5 * 60 * 1000, 1000, 55 * 60 * 1000);
    this.maxQueueEvents = boundedInteger(options.maxQueueEvents, 200, 1, 1000);
    this.maxQueueBytes = boundedInteger(options.maxQueueBytes, 256 * 1024, 8192, 1024 * 1024);
    this.maxBatchEvents = boundedInteger(options.maxBatchEvents, 20, 1, 50);
    this.maxBatchBytes = boundedInteger(options.maxBatchBytes, 60 * 1024, 10 * 1024, 60 * 1024);
    this.sessionTimeout = boundedInteger(
      options.sessionTimeoutMs,
      30 * 60 * 1000,
      60000,
      24 * 60 * 60 * 1000,
    );
    this.autoPageViews = options.autoPageViews === true;
    this.autoIdentityLink = options.autoIdentityLink === true;
    this.storage = options.storage === undefined ? storageDefault() : options.storage;
    this.storageKey = `evia:${this.projectId}:${this.environment}:${this.writeKey}:anonymous`;
    let stored: string | null = null;
    try {
      stored = this.storage?.getItem(this.storageKey) ?? null;
    } catch {
      /* memory fallback */
    }
    this.anonymousId = stored && uuidPattern.test(stored) ? stored : this.uuid();
    this.persistAnonymous();
    this.tabId = this.uuid();
    this.sessionId = this.uuid();
    this.lastActivity = this.clock.now();
    this.page = this.newPage({ navigationType: 'initial' });
    if (
      options.lifecycle !== false &&
      typeof document !== 'undefined' &&
      typeof window !== 'undefined'
    ) {
      const hidden = () => {
        if (document.visibilityState === 'hidden') void this.flush({ keepalive: true });
      };
      const pagehide = () => {
        void this.flush({ keepalive: true });
      };
      document.addEventListener('visibilitychange', hidden);
      window.addEventListener('pagehide', pagehide);
      this.cleanup.push(
        () => document.removeEventListener('visibilitychange', hidden),
        () => window.removeEventListener('pagehide', pagehide),
      );
    }
    if (this.autoPageViews) this.emitAutoPageView();
  }

  get pending(): number {
    return this.queue.length;
  }

  private assertEnabled(): void {
    if (this.destroyed) throw new EviaSDKError('CLIENT_DESTROYED');
    if (!this.enabled) throw new EviaSDKError('CLIENT_DISABLED');
  }

  private persistAnonymous(): void {
    try {
      this.storage?.setItem(this.storageKey, this.anonymousId);
    } catch {
      /* memory fallback */
    }
  }

  init(): Promise<PublicConfig> {
    return this.loadConfig(false);
  }
  refreshConfig(): Promise<PublicConfig> {
    return this.loadConfig(true);
  }

  private loadConfig(force: boolean): Promise<PublicConfig> {
    try {
      this.assertEnabled();
    } catch (error) {
      return Promise.reject(error);
    }
    if (this.configFlight) return this.configFlight;
    if (!force && this.config && this.configExpires > this.clock.now())
      return Promise.resolve(this.config);
    const generation = this.generation;
    const url = new URL(`${this.endpoint}/v1/config`);
    url.searchParams.set('write_key', this.writeKey);
    if (this.config)
      url.searchParams.set('current_release_revision', String(this.config.release_revision));
    const headers: Record<string, string> = {};
    if (this.etag) headers['If-None-Match'] = this.etag;
    let flight: Promise<PublicConfig>;
    flight = this.transport
      .request(url.href, { method: 'GET', headers }, async (response) => {
        if (response.status === 304 && this.config) return { config: this.config, etag: this.etag };
        if (response.status !== 200) {
          void response.body?.cancel().catch(() => {});
          throw new EviaSDKError(`CONFIG_HTTP_${response.status}`);
        }
        return {
          config: freezeConfig(
            validateConfig(await readJSON(response, 1024 * 1024), this.projectId, this.environment),
          ),
          etag: response.headers.get('ETag') ?? undefined,
        };
      })
      .then(({ config, etag }) => {
        if (generation !== this.generation || !this.enabled)
          throw new EviaSDKError('REQUEST_ABORTED');
        if (this.config && config.release_revision < this.config.release_revision)
          throw new EviaSDKError('CONFIG_REVISION_REGRESSION');
        this.config = config;
        this.etag = etag && /^"[a-f0-9]{64}"$/.test(etag) ? etag : undefined;
        this.configExpires = this.clock.now() + this.configTtl;
        return config;
      })
      .catch((error: unknown) => {
        if (generation === this.generation) this.configExpires = 0;
        throw error;
      })
      .finally(() => {
        if (this.configFlight === flight) this.configFlight = undefined;
      });
    this.configFlight = flight;
    return flight;
  }

  emit(eventKey: string, properties: Properties = {}, options: EventOptions = {}): Emission {
    this.assertEnabled();
    if (typeof eventKey !== 'string' || !customEventPattern.test(eventKey))
      throw new EviaSDKError('INVALID_CUSTOM_EVENT_KEY');
    return this.enqueue(eventKey, properties, options);
  }

  // reportOperationFailure enqueues one reserved sys.operation_failed event. The
  // payload is rebuilt from bounded, validated fields rather than spread from the
  // caller object, so arbitrary data, raw exceptions and request bodies can never
  // enter the event (the schema sets additionalProperties=false).
  reportOperationFailure(failure: OperationFailure): Emission {
    this.assertEnabled();
    if (failure === null || typeof failure !== 'object')
      throw new EviaSDKError('INVALID_OPERATION_FAILURE');
    requireText(failure.operation, 128, true);
    if (!operationErrorCategories.has(failure.errorCategory))
      throw new EviaSDKError('INVALID_ERROR_CATEGORY');
    if (typeof failure.retryable !== 'boolean') throw new EviaSDKError('INVALID_RETRYABLE');
    const properties: Record<string, string | boolean> = {
      operation: failure.operation,
      error_category: failure.errorCategory,
      retryable: failure.retryable,
    };
    if (failure.errorCode !== undefined) {
      requireText(failure.errorCode, 128, true);
      properties.error_code = failure.errorCode;
    }
    return this.enqueue(operationFailedEventKey, properties, {});
  }

  // enqueue builds and queues one envelope. Public callers validate the custom
  // event key in emit(); automatic collection passes the reserved sys.* key.
  private enqueue(eventKey: string, properties: Properties, options: EventOptions): Emission {
    this.assertEnabled();
    if (this.queue.length >= this.maxQueueEvents) throw new EviaSDKError('QUEUE_FULL');
    const copied = copyProperties(properties);
    if (options.operationId !== undefined) requireText(options.operationId, 128, true);
    if (this.sequence >= Number.MAX_SAFE_INTEGER) throw new EviaSDKError('SEQUENCE_EXHAUSTED');
    this.touchSession();
    if (options.navigation) this.checkLink(options.navigation);
    const eventId = this.uuid();
    const draft: Entry['draft'] = {
      event_id: eventId,
      event_key: eventKey,
      occurred_at: new Date(this.clock.now()).toISOString(),
      identity: {
        anonymous_id: this.anonymousId,
        ...(this.uid === undefined ? {} : { uid: this.uid }),
      },
      context: {
        ...this.page,
        sequence: ++this.sequence,
        ...(options.operationId === undefined ? {} : { operation_id: options.operationId }),
        ...(options.navigation ? { navigation_id: options.navigation.navigationId } : {}),
      },
      sdk: { name: 'evia-browser', version: '0.1.0' },
      properties: copied,
    };
    const reserved = byteLength(
      JSON.stringify({
        ...draft,
        schema_version: Number.MAX_SAFE_INTEGER,
        release_revision: Number.MAX_SAFE_INTEGER,
      }),
    );
    if (reserved > 8192) throw new EviaSDKError('EVENT_TOO_LARGE');
    if (this.queueBytes + reserved > this.maxQueueBytes) throw new EviaSDKError('QUEUE_FULL');
    let settle!: Entry['settle'];
    const result = new Promise<EventOutcome>((resolve) => {
      settle = resolve;
    });
    const entry: Entry = {
      eventId,
      draft,
      createdAt: this.clock.now(),
      bytes: reserved,
      attempts: 0,
      ambiguous: false,
      settle,
    };
    this.queue.push(entry);
    this.queueBytes += reserved;
    this.schedule(this.maxAge);
    const generation = this.generation;
    void this.init()
      .then((config) => {
        if (generation !== this.generation || !this.enabled || !this.queue.includes(entry)) return;
        const event = config.events.find((candidate) => candidate.event_key === eventKey);
        if (!event) {
          this.finish(entry, 'rejected', 'EVENT_NOT_PUBLISHED', false);
          return;
        }
        entry.json = JSON.stringify({
          ...draft,
          schema_version: event.schema_version,
          release_revision: config.release_revision,
        });
        this.schedule(this.flushInterval);
      })
      .catch((error: unknown) => {
        if (generation === this.generation && this.queue.includes(entry))
          this.finish(
            entry,
            'dropped',
            error instanceof EviaSDKError ? error.code : 'CONFIG_UNAVAILABLE',
            true,
          );
      });
    return Object.freeze({ eventId, result });
  }

  identify(uid: string): void {
    this.assertEnabled();
    requireText(uid, 128, true);
    if (uid.includes('\0')) throw new EviaSDKError('INVALID_UID');
    // Assign before enqueue so the identity-link envelope carries the new uid
    // together with the anonymous id, exactly as the contract fixture specifies.
    this.uid = uid;
    this.emitIdentityLink();
  }

  reset(): void {
    this.assertEnabled();
    this.uid = undefined;
    this.anonymousId = this.uuid();
    this.persistAnonymous();
    this.sessionId = this.uuid();
    this.lastActivity = this.clock.now();
    this.page = this.newPage({ navigationType: 'initial' });
    this.emitAutoPageView();
  }

  private touchSession(): void {
    const now = this.clock.now();
    if (now - this.lastActivity >= this.sessionTimeout || now < this.lastActivity) {
      this.sessionId = this.uuid();
      this.page = this.newPage({
        navigationType: 'initial',
        routeName: this.page.route_name,
        routeTemplate: this.page.route_template,
        sourceModule: this.page.source_module,
      });
    }
    this.lastActivity = now;
  }

  private checkLink(link: NavigationLink): void {
    if (!uuidPattern.test(link.navigationId) || link.sourcePageViewId !== this.page.page_view_id)
      throw new EviaSDKError('INVALID_NAVIGATION_LINK');
  }

  beginNavigation(): NavigationLink {
    this.assertEnabled();
    this.touchSession();
    return Object.freeze({ navigationId: this.uuid(), sourcePageViewId: this.page.page_view_id });
  }

  private newPage(options: PageOptions, previous?: string): Page {
    for (const [value, bound] of [
      [options.routeName, 128],
      [options.routeTemplate, 256],
      [options.sourceModule, 64],
    ] as const)
      if (value !== undefined) requireText(value, bound);
    if (
      options.navigationType !== undefined &&
      !['initial', 'push', 'replace', 'pop', 'restore'].includes(options.navigationType)
    )
      throw new EviaSDKError('INVALID_NAVIGATION_TYPE');
    return Object.freeze({
      session_id: this.sessionId,
      tab_id: this.tabId,
      page_view_id: this.uuid(),
      navigation_id: options.link?.navigationId ?? this.uuid(),
      navigation_type: options.navigationType ?? 'push',
      ...(previous
        ? {
            previous_page_view_id: previous,
            source_page_view_id: options.link?.sourcePageViewId ?? previous,
          }
        : {}),
      ...(options.routeName === undefined ? {} : { route_name: options.routeName }),
      ...(options.routeTemplate === undefined ? {} : { route_template: options.routeTemplate }),
      ...(options.sourceModule === undefined ? {} : { source_module: options.sourceModule }),
    });
  }

  // emitSystemEvent records one reserved sys.* event with empty properties.
  // Backpressure must never break the underlying SDK action (navigation or
  // identify), so a full queue drops the automatic event; an unpublished system
  // event still fails closed via the configuration check (EVENT_NOT_PUBLISHED).
  private emitSystemEvent(eventKey: string, enabled: boolean): void {
    if (!enabled || this.destroyed || !this.enabled) return;
    try {
      this.enqueue(eventKey, {}, {});
    } catch (error) {
      if (error instanceof EviaSDKError && error.code === 'QUEUE_FULL') return;
      throw error;
    }
  }

  // emitAutoPageView records one reserved sys.page_view for the current page.
  private emitAutoPageView(): void {
    this.emitSystemEvent(pageViewEventKey, this.autoPageViews);
  }

  // emitIdentityLink records one reserved sys.identity_link after identify().
  private emitIdentityLink(): void {
    this.emitSystemEvent(identityLinkEventKey, this.autoIdentityLink);
  }

  navigate(options: PageOptions = {}): Readonly<Page> {
    this.assertEnabled();
    this.touchSession();
    if (options.link) this.checkLink(options.link);
    this.page = this.newPage(options, this.page.page_view_id);
    this.emitAutoPageView();
    return this.page;
  }

  private finish(
    entry: Entry,
    status: EventOutcome['status'],
    code?: string,
    retryable?: boolean,
    receiptId?: string,
  ): void {
    const index = this.queue.indexOf(entry);
    if (index < 0) return;
    this.queue.splice(index, 1);
    this.queueBytes -= entry.bytes;
    if (this.queue.length === 0 && this.timer !== undefined) {
      this.clock.clearTimeout(this.timer);
      this.timer = undefined;
      this.timerAt = Infinity;
    }
    entry.settle({
      eventId: entry.eventId,
      status,
      attempts: entry.attempts,
      ...(code ? { code } : {}),
      ...(retryable === undefined ? {} : { retryable }),
      ...(receiptId ? { receiptId } : {}),
    });
  }

  private schedule(delay: number): void {
    if (!this.enabled || this.destroyed || this.queue.length === 0) return;
    const at = this.clock.now() + Math.max(0, delay);
    if (this.timer !== undefined && this.timerAt <= at) return;
    if (this.timer !== undefined) this.clock.clearTimeout(this.timer);
    this.timerAt = at;
    this.timer = this.clock.setTimeout(
      () => {
        this.timer = undefined;
        this.timerAt = Infinity;
        void this.flush();
      },
      Math.max(0, delay),
    );
  }

  flush(options: { keepalive?: boolean } = {}): Promise<FlushResult> {
    if (!this.enabled || this.destroyed) return Promise.resolve({ pending: this.pending });
    if (this.flushFlight) return this.flushFlight;
    const generation = this.generation;
    let flight: Promise<FlushResult>;
    flight = this.drain(generation, options.keepalive === true).finally(() => {
      if (this.flushFlight === flight) this.flushFlight = undefined;
      if (generation === this.generation && this.queue.length) {
        const untilExpiry = Math.min(
          ...this.queue.map((entry) => entry.createdAt + this.maxAge - this.clock.now()),
        );
        const untilReady = this.queue.some((entry) => entry.json)
          ? Math.max(this.flushInterval, this.nextAttempt - this.clock.now())
          : Infinity;
        this.schedule(Math.min(untilExpiry, untilReady));
      }
    });
    this.flushFlight = flight;
    return flight;
  }

  private makeBatch(): Batch | undefined {
    const entries: Entry[] = [];
    const id = this.uuid();
    const prefix = `{"protocol_version":1,"batch_id":${JSON.stringify(id)},"write_key":${JSON.stringify(this.writeKey)},"sent_at":${JSON.stringify(new Date(this.clock.now()).toISOString())},"events":[`;
    let bytes = byteLength(prefix) + 2;
    for (const entry of this.queue) {
      if (!entry.json) break;
      const extra = byteLength(entry.json) + (entries.length ? 1 : 0);
      if (entries.length >= this.maxBatchEvents || bytes + extra > this.maxBatchBytes) break;
      entries.push(entry);
      bytes += extra;
    }
    return entries.length
      ? { id, body: prefix + entries.map((entry) => entry.json).join(',') + ']}', entries }
      : undefined;
  }

  private async drain(generation: number, keepalive: boolean): Promise<FlushResult> {
    // Let synchronously resolved configuration preparation finish first.
    await Promise.resolve();
    for (let round = 0; round < (keepalive ? 1 : 4); round++) {
      if (generation !== this.generation || !this.enabled) break;
      const now = this.clock.now();
      for (const entry of [...this.queue])
        if (now - entry.createdAt >= this.maxAge)
          this.finish(
            entry,
            entry.ambiguous ? 'uncertain' : entry.attempts ? 'rejected' : 'dropped',
            'EVENT_EXPIRED',
            false,
          );
      if (this.batch?.entries.some((entry) => !this.queue.includes(entry))) this.batch = undefined;
      if (!this.queue.length || now < this.nextAttempt) break;
      const batch = this.batch ?? this.makeBatch();
      if (!batch) break;
      this.batch = batch;
      for (const entry of batch.entries) entry.attempts++;
      try {
        const answer = await this.transport.request(
          `${this.endpoint}/v1/collect`,
          {
            method: 'POST',
            headers: { 'Content-Type': 'text/plain;charset=UTF-8' },
            body: batch.body,
            // Lifecycle transitions cannot upgrade an already submitted fetch.
            // Every bounded collection request is keepalive-capable from start.
            keepalive: true,
          },
          async (response) => {
            const delay = retryAfter(response, this.clock.now());
            if (response.status === 202 || response.status === 207 || response.status === 422) {
              const receipt = validateReceipt(
                await readJSON(response, 64 * 1024),
                batch.id,
                batch.entries.map((entry) => entry.eventId),
              );
              if (
                response.status === 422 &&
                receipt.results.some((result) => result.status !== 'rejected')
              )
                throw new EviaSDKError('INVALID_RECEIPT');
              return { receipt, delay };
            }
            void response.body?.cancel().catch(() => {});
            return { status: response.status, delay };
          },
        );
        if (generation !== this.generation || !this.enabled) break;
        if (answer.receipt) {
          for (const result of answer.receipt.results) {
            const entry = batch.entries[result.index]!;
            if (result.status === 'accepted')
              this.finish(entry, 'accepted', undefined, undefined, answer.receipt.receipt_id);
            else if (!result.error!.retryable)
              this.finish(
                entry,
                entry.ambiguous ? 'uncertain' : 'rejected',
                result.error!.code,
                false,
                answer.receipt.receipt_id,
              );
            else if (entry.attempts >= this.maxAttempts)
              this.finish(
                entry,
                entry.ambiguous ? 'uncertain' : 'rejected',
                result.error!.code,
                true,
                answer.receipt.receipt_id,
              );
          }
          this.batch = undefined;
          if (batch.entries.some((entry) => this.queue.includes(entry))) {
            this.defer(batch, answer.delay);
            break;
          }
          this.nextAttempt = 0;
        } else {
          const status = answer.status!;
          const retryable =
            status === 408 ||
            status === 425 ||
            status === 429 ||
            status >= 500 ||
            (status >= 200 && status < 300);
          const ambiguous =
            status !== 429 && (status === 408 || status >= 500 || (status >= 200 && status < 300));
          for (const entry of batch.entries) {
            entry.ambiguous ||= ambiguous;
            if (!retryable || entry.attempts >= this.maxAttempts)
              this.finish(
                entry,
                entry.ambiguous ? 'uncertain' : 'rejected',
                `COLLECT_HTTP_${status}`,
                retryable,
              );
          }
          if (!retryable || batch.entries.every((entry) => !this.queue.includes(entry)))
            this.batch = undefined;
          else {
            this.defer(batch, answer.delay);
            break;
          }
        }
      } catch {
        if (generation !== this.generation || !this.enabled) break;
        for (const entry of batch.entries) {
          entry.ambiguous = true;
          if (entry.attempts >= this.maxAttempts)
            this.finish(entry, 'uncertain', 'RETRY_EXHAUSTED', true);
        }
        if (batch.entries.every((entry) => !this.queue.includes(entry))) this.batch = undefined;
        else {
          this.defer(batch);
          break;
        }
      }
    }
    return { pending: this.pending };
  }

  private defer(batch: Batch, serverDelay?: number): void {
    const attempts = Math.max(...batch.entries.map((entry) => entry.attempts));
    this.nextAttempt =
      this.clock.now() +
      Math.max(serverDelay ?? 0, Math.min(this.retryBase * 2 ** (attempts - 1), 60000));
    const expiresIn = Math.min(
      ...this.queue.map((entry) => entry.createdAt + this.maxAge - this.clock.now()),
    );
    this.schedule(Math.min(this.nextAttempt - this.clock.now(), Math.max(0, expiresIn)));
  }

  disable(): void {
    if (this.destroyed || !this.enabled) return;
    this.enabled = false;
    this.generation++;
    this.configExpires = 0;
    this.configFlight = undefined;
    this.flushFlight = undefined;
    this.transport.abort();
    if (this.timer !== undefined) this.clock.clearTimeout(this.timer);
    this.timer = undefined;
    this.timerAt = Infinity;
    this.nextAttempt = 0;
    this.batch = undefined;
    for (const entry of [...this.queue])
      this.finish(entry, entry.attempts ? 'uncertain' : 'dropped', 'CLIENT_DISABLED', false);
  }

  enable(): Promise<PublicConfig> {
    if (this.destroyed) return Promise.reject(new EviaSDKError('CLIENT_DESTROYED'));
    this.enabled = true;
    return this.init();
  }

  destroy(): void {
    if (this.destroyed) return;
    this.disable();
    this.destroyed = true;
    for (const remove of this.cleanup) remove();
    this.cleanup.length = 0;
  }
}
