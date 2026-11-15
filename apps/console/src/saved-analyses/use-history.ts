import { useCallback, useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, type SavedAnalysisVersion } from '@evia/api-client';
import type { CatalogPageProps } from '../catalog';
import { savedAnalysesKey } from './types';

export type SavedAnalysisListView = 'active' | 'archived';
type HistoryOptions = {
  context: CatalogPageProps;
  identity: string;
  analysisID?: string;
  tab: string;
  canRead: boolean;
  denied: boolean;
};
/** Keep the last displayed immutable version while another selection is being fetched. */
export function useSavedAnalysisHistory({
  context,
  identity,
  analysisID,
  tab,
  canRead,
  denied,
}: HistoryOptions) {
  const { project, environment, session } = context;
  const prefix = savedAnalysesKey(project.id, environment, session.user_id);
  const cache = useQueryClient();
  const [cursors, setCursors] = useState<string[]>([]),
    [versionCursors, setVersionCursors] = useState<string[]>([]);
  const [archiveView, setArchiveView] = useState<SavedAnalysisListView>('active');
  const [versionNumber, setVersionNumber] = useState<number>();
  const [displayVersion, setDisplayVersion] = useState<SavedAnalysisVersion>();
  const list = useQuery({
    queryKey: [...prefix, 'list', archiveView, cursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listSavedAnalyses(
        project.id,
        { environment, view: archiveView, cursor: cursors.at(-1), limit: 20 },
        signal,
      ),
    enabled: canRead && !denied,
  });
  const versions = useQuery({
    queryKey: [...prefix, 'versions', analysisID, versionCursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listSavedAnalysisVersions(
        project.id,
        analysisID!,
        { cursor: versionCursors.at(-1), limit: 20 },
        signal,
      ),
    enabled: !!analysisID && tab === 'versions' && !denied,
  });
  const selectedVersion = useQuery({
    queryKey: [...prefix, 'version', analysisID, versionNumber],
    queryFn: ({ signal }) =>
      api.getSavedAnalysisVersion(project.id, analysisID!, versionNumber!, signal),
    enabled: !!analysisID && !!versionNumber && tab === 'versions' && !denied,
  });
  const reset = useCallback(() => {
    setCursors([]);
    setVersionCursors([]);
    setVersionNumber(undefined);
    setDisplayVersion(undefined);
    setArchiveView('active');
  }, []);
  useEffect(reset, [identity, reset]);
  useEffect(() => {
    if (
      selectedVersion.data?.analysis_id === analysisID &&
      selectedVersion.data?.environment === environment
    )
      setDisplayVersion(selectedVersion.data);
  }, [selectedVersion.data, analysisID, environment]);
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
  function changeView(next: SavedAnalysisListView) {
    if (next === archiveView) return;
    setArchiveView(next);
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
