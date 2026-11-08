import { useInfiniteQuery, useQueries } from '@tanstack/react-query';
import { api, type Environment, type SavedAnalysisTemplate } from '@evia/api-client';
import { Button, ErrorPanel, LoadingPanel } from '@evia/design-system';
import { publishedFields, systemFields, type PublishedField } from '../analysis-model';
import { savedPins } from '../saved-analysis-model';
import type { EditorSection } from './types';

type CatalogScope = {
  projectID: string;
  environment: Environment;
  actorID?: string;
  query: SavedAnalysisTemplate;
  section: EditorSection | null;
  readOnly: boolean;
};
export function useEditorCatalog({
  projectID,
  environment,
  actorID,
  query,
  section,
  readOnly,
}: CatalogScope) {
  const catalog = useInfiniteQuery({
    queryKey: ['saved-analysis-editor-events', actorID ?? '', projectID, environment],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam, signal }) =>
      api.listEventDefinitions(projectID, { environment, limit: 100, cursor: pageParam }, signal),
    getNextPageParam: (page) => page.next_cursor ?? undefined,
    enabled: !readOnly && !!section && section !== 'json',
  });
  const events =
    catalog.data?.pages
      .flatMap((page) => page.items)
      .filter((event) => event.published_versions.length) ?? [];
  const pins = savedPins(query).filter((pin) => pin.event_key && pin.schema_version > 0);
  const versions = useQueries({
    queries: pins.map((pin) => ({
      queryKey: [
        'saved-analysis-editor-version',
        actorID ?? '',
        projectID,
        environment,
        pin.event_key,
        pin.schema_version,
      ],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        api.getEventVersion(projectID, pin.event_key, pin.schema_version, signal),
      enabled: !!section && ['metrics', 'grouping', 'filters'].includes(section),
    })),
  });
  const fields: PublishedField[] = [
    ...systemFields.map((field) => ({
      id: `system:${field.key}`,
      label: field.label,
      type: field.type,
      reference: { kind: 'system' as const, key: field.key },
    })),
    ...versions.flatMap((version, index) =>
      version.data
        ? publishedFields(
            version.data.definition.schema,
            pins[index]!.event_key,
            pins[index]!.schema_version,
          )
        : [],
    ),
  ];
  return {
    events,
    fields,
    notice: {
      eventsPending: catalog.isPending,
      eventsError: catalog.error,
      eventCount: events.length,
      hasNext: catalog.hasNextPage,
      fetchingNext: catalog.isFetchingNextPage,
      retryEvents: () => {
        void catalog.refetch();
      },
      loadNext: () => {
        void catalog.fetchNextPage();
      },
      fieldsPending:
        versions.some((result) => result.isPending) &&
        !!section &&
        ['metrics', 'grouping', 'filters'].includes(section),
      fieldsError: versions.find((result) => result.error)?.error,
      retryFields: () =>
        versions.forEach((result) => {
          if (result.error) void result.refetch();
        }),
    },
  };
}

export function CatalogNotice({
  eventsPending,
  eventsError,
  eventCount,
  hasNext,
  fetchingNext,
  retryEvents,
  loadNext,
  fieldsPending,
  fieldsError,
  retryFields,
}: {
  eventsPending: boolean;
  eventsError: Error | null;
  eventCount: number;
  hasNext: boolean;
  fetchingNext: boolean;
  retryEvents: () => void;
  loadNext: () => void;
  fieldsPending: boolean;
  fieldsError?: Error | null;
  retryFields: () => void;
}) {
  return (
    <>
      {eventsPending ? (
        <LoadingPanel label="正在加载已发布事件" />
      ) : eventsError ? (
        <ErrorPanel message={eventsError.message} retry={retryEvents} />
      ) : eventCount === 0 ? (
        <p>当前环境尚无已发布事件，请先发布事件定义。</p>
      ) : null}
      {hasNext && (
        <Button disabled={fetchingNext} onClick={loadNext} loading={fetchingNext}>
          加载更多事件
        </Button>
      )}
      {fieldsPending && <LoadingPanel label="正在加载固定版本字段" />}
      {fieldsError && <ErrorPanel message={fieldsError.message} retry={retryFields} />}
    </>
  );
}
