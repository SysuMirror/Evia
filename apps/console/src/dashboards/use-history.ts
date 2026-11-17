import { useCallback, useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, type DashboardVersion } from '@evia/api-client';
import type { CatalogPageProps } from '../catalog';
import { normalizeDashboardSearch } from './model';
import { dashboardsKey, type DashboardFolderScope } from './types';

/** Debounce window for the title search box so typing never fires a request per keystroke. */
const searchDebounceMs = 300;

export type DashboardListView = 'active' | 'archived';
type HistoryOptions = {
  context: CatalogPageProps;
  identity: string;
  dashboardID?: string;
  tab: string;
  canRead: boolean;
  denied: boolean;
};
/** Keep the last displayed immutable version while another selection is being fetched. */
export function useDashboardHistory({
  context,
  identity,
  dashboardID,
  tab,
  canRead,
  denied,
}: HistoryOptions) {
  const { project, environment, session } = context;
  const prefix = dashboardsKey(project.id, environment, session.user_id);
  const cache = useQueryClient();
  const [cursors, setCursors] = useState<string[]>([]),
    [versionCursors, setVersionCursors] = useState<string[]>([]);
  const [archiveView, setArchiveView] = useState<DashboardListView>('active');
  const [folderScope, setFolderScope] = useState<DashboardFolderScope>('');
  // searchInput mirrors the textbox immediately; search is the debounced,
  // normalized term actually bound to the request and to any page cursor.
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const [versionNumber, setVersionNumber] = useState<number>();
  const [displayVersion, setDisplayVersion] = useState<DashboardVersion>();
  useEffect(() => {
    const timer = setTimeout(() => {
      const next = normalizeDashboardSearch(searchInput);
      setSearch((previous) => (next === previous ? previous : next));
    }, searchDebounceMs);
    return () => clearTimeout(timer);
  }, [searchInput]);
  // A new predicate invalidates any cursor collected under the previous term.
  useEffect(() => {
    setCursors([]);
  }, [search]);
  const list = useQuery({
    queryKey: [...prefix, 'list', archiveView, folderScope, search, cursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listDashboards(
        project.id,
        {
          environment,
          view: archiveView,
          folderId: folderScope || undefined,
          search: search || undefined,
          cursor: cursors.at(-1),
          limit: 20,
        },
        signal,
      ),
    enabled: canRead && !denied,
  });
  const versions = useQuery({
    queryKey: [...prefix, 'versions', dashboardID, versionCursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listDashboardVersions(
        project.id,
        dashboardID!,
        { cursor: versionCursors.at(-1), limit: 20 },
        signal,
      ),
    enabled: !!dashboardID && tab === 'versions' && !denied,
  });
  const selectedVersion = useQuery({
    queryKey: [...prefix, 'version', dashboardID, versionNumber],
    queryFn: ({ signal }) =>
      api.getDashboardVersion(project.id, dashboardID!, versionNumber!, signal),
    enabled: !!dashboardID && !!versionNumber && tab === 'versions' && !denied,
  });
  const reset = useCallback(() => {
    setCursors([]);
    setVersionCursors([]);
    setVersionNumber(undefined);
    setDisplayVersion(undefined);
    setArchiveView('active');
    setFolderScope('');
    setSearchInput('');
    setSearch('');
  }, []);
  useEffect(reset, [identity, reset]);
  useEffect(() => {
    if (
      selectedVersion.data?.dashboard_id === dashboardID &&
      selectedVersion.data?.environment === environment
    )
      setDisplayVersion(selectedVersion.data);
  }, [selectedVersion.data, dashboardID, environment]);
  const versionItems = versions.data?.items ?? [];
  const options = [...versionItems];
  if (
    selectedVersion.data &&
    !options.some((item) => item.version === selectedVersion.data.version)
  )
    options.push(selectedVersion.data);
  options.sort((left, right) => left.version - right.version);
  function openDraft(publishedVersion: number) {
    setVersionNumber(publishedVersion || undefined);
    setVersionCursors([]);
    setDisplayVersion(undefined);
  }
  function create() {
    setVersionNumber(undefined);
    setDisplayVersion(undefined);
  }
  function changeView(next: DashboardListView) {
    if (next === archiveView) return;
    setArchiveView(next);
    setCursors([]);
  }
  function changeFolderScope(next: DashboardFolderScope) {
    if (next === folderScope) return;
    setFolderScope(next);
    setCursors([]);
  }
  function invalidateList() {
    void cache.invalidateQueries({ queryKey: prefix });
  }
  function published(version: number) {
    setVersionNumber(version);
    setVersionCursors([]);
  }
  return {
    list,
    versions,
    selectedVersion,
    displayVersion,
    versionNumber,
    options,
    reset,
    openDraft,
    create,
    published,
    setVersionNumber,
    archiveView,
    changeView,
    folderScope,
    changeFolderScope,
    searchInput,
    search,
    changeSearch: setSearchInput,
    invalidateList,
    listPage: cursors.length + 1,
    previousList: () => setCursors((previous) => previous.slice(0, -1)),
    nextList: () => {
      if (list.data?.next_cursor) setCursors((previous) => [...previous, list.data!.next_cursor!]);
    },
    firstList: () => setCursors([]),
    versionPage: versionCursors.length + 1,
    previousVersion: () => setVersionCursors((previous) => previous.slice(0, -1)),
    nextVersion: () => {
      if (versions.data?.next_cursor)
        setVersionCursors((previous) => [...previous, versions.data!.next_cursor!]);
    },
  };
}
