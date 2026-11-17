import { useCallback, useEffect, useState } from 'react';
import { ApiError, api, type Dashboard, type DashboardDefinition } from '@evia/api-client';
import { useToast } from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import type { SavedRunWork } from './run';
import { useUnsavedWork } from '../use-unsaved-work';
import { useDashboardRequest } from './use-scoped-request';
import { useDashboardDraft } from './use-draft';
import { useDashboardFolders } from './use-dashboard-folders';
import { useDashboardHistory } from './use-history';
import { useDashboardPublication } from './use-publication';

export function useDashboardsController(context: CatalogPageProps) {
  const { project, environment, session } = context;
  const toast = useToast();
  const canRead = project.permissions.includes('project.read'),
    canWrite = project.permissions.includes('project.write');
  const identity = JSON.stringify([
    project.id,
    environment,
    session.user_id,
    session.csrf_token,
    [...project.permissions].sort(),
  ]);
  const [tab, setTab] = useState('draft');
  const [runWork, setRunWork] = useState<SavedRunWork>({ dirty: false, busy: false });
  const request = useDashboardRequest(context, identity, canRead);
  const draft = useDashboardDraft({
    context,
    identity,
    canWrite,
    busy: request.busy,
    perform: request.perform,
    isPending: request.isPending,
  });
  const history = useDashboardHistory({
    context,
    identity,
    dashboardID: draft.workspace?.draft?.dashboard_id,
    tab,
    canRead,
    denied: request.denied,
  });
  const publication = useDashboardPublication({
    context,
    identity,
    workspace: draft.workspace,
    dirty: draft.dirty,
    editorValid: draft.editorValid,
    editorPending: draft.editorPending,
    canWrite,
    busy: request.busy,
    perform: request.perform,
    isPending: request.isPending,
    onPublished: (version) => {
      draft.markPublished(version);
      history.published(version.version);
      setTab('versions');
    },
  });
  const folders = useDashboardFolders({
    context,
    canRead,
    denied: request.denied,
    perform: request.perform,
  });
  const guard = useUnsavedWork({
    dirty: draft.dirty || !!draft.createAttempt || !!publication.attempt || runWork.dirty,
    busy: !!request.busy || runWork.busy,
  });
  useEffect(() => {
    setRunWork({ dirty: false, busy: false });
  }, [identity]);
  useEffect(() => {
    for (const failure of [
      history.list.error,
      history.versions.error,
      history.selectedVersion.error,
    ])
      if (failure instanceof ApiError && [401, 403].includes(failure.status)) {
        request.deny(failure);
        break;
      }
  }, [history.list.error, history.versions.error, history.selectedVersion.error, request.deny]);
  useEffect(() => {
    if (request.denied) {
      draft.reset();
      publication.reset();
    }
  }, [request.denied, draft.reset, publication.reset]);
  const onRunWork = useCallback((work: SavedRunWork) => setRunWork(work), []);
  function openDraft(analysis: Dashboard) {
    if (request.isPending()) return;
    guard.requestLeave(() =>
      draft.open(analysis, (loaded) => {
        publication.resetReview();
        history.openDraft(loaded.published_version);
        setTab('draft');
      }),
    );
  }
  function create() {
    if (request.isPending()) return;
    // A specific folder selected in the sidebar pre-fills the new dashboard's
    // location; "all" and "root" both start at the root.
    const initialFolder =
      history.folderScope && history.folderScope !== 'root' ? history.folderScope : '';
    guard.requestLeave(() => {
      draft.create(initialFolder);
      setTab('draft');
      history.create();
      publication.resetReview();
      request.setError(null);
    });
  }
  function close() {
    guard.requestLeave(() => {
      draft.close();
      setRunWork({ dirty: false, busy: false });
      request.setError(null);
      publication.resetReview();
    });
  }
  function changeTab(next: string) {
    if (request.busy || runWork.busy || draft.createAttempt || publication.attempt || next === tab)
      return;
    if (draft.editorPending || (tab === 'versions' && runWork.dirty))
      guard.requestLeave(() => setTab(next));
    else setTab(next);
  }
  function changeDefinition(definition: DashboardDefinition) {
    draft.changeDefinition(definition);
    publication.resetReview();
  }
  function save() {
    draft.save(!!publication.attempt, publication.resetReview, history.firstList);
  }
  function readLatest() {
    draft.readLatest(publication.resetReview);
  }
  function archive(dashboard: Dashboard) {
    if (request.isPending()) return;
    void request.perform(
      '归档看板',
      (signal) =>
        api.archiveDashboard(project.id, dashboard.dashboard_id, session.csrf_token, signal),
      () => {
        toast.success(`看板「${dashboard.title}」已归档，可在已归档视图中恢复。`);
        history.invalidateList();
      },
    );
  }
  function unarchive(dashboard: Dashboard) {
    if (request.isPending()) return;
    void request.perform(
      '恢复看板',
      (signal) =>
        api.unarchiveDashboard(project.id, dashboard.dashboard_id, session.csrf_token, signal),
      () => {
        toast.success(`看板「${dashboard.title}」已恢复到活跃列表。`);
        history.invalidateList();
      },
    );
  }
  function moveDashboard(dashboard: Dashboard, folderID: string) {
    if (request.isPending()) return;
    // The move rides the draft PATCH, so the current definition and revision
    // must be read before applying the full-replacement folder assignment.
    void request.perform(
      '移动看板',
      async (signal) => {
        const current = await api.getDashboardDraft(project.id, dashboard.dashboard_id, signal);
        return api.updateDashboardDraft(
          project.id,
          dashboard.dashboard_id,
          current.draft_revision,
          { definition: current.definition, folder_id: folderID || undefined },
          session.csrf_token,
          signal,
        );
      },
      () => {
        toast.success(`看板「${dashboard.title}」已移动。`);
        history.invalidateList();
        folders.invalidate();
      },
    );
  }
  function rebaseLocal() {
    draft.rebaseLocal();
    publication.resetReview();
    request.setError(null);
  }
  function adoptLatest() {
    guard.requestLeave(() => {
      draft.adoptLatest();
      publication.resetReview();
      request.setError(null);
    });
  }
  const frozen = !!request.busy || !!draft.createAttempt || !!publication.attempt || runWork.busy;
  return {
    canRead,
    canWrite,
    tab,
    runWork,
    request,
    draft,
    history,
    publication,
    folders,
    frozen,
    onRunWork,
    openDraft,
    create,
    close,
    changeTab,
    changeDefinition,
    save,
    readLatest,
    archive,
    unarchive,
    moveDashboard,
    rebaseLocal,
    adoptLatest,
    confirmation: guard.confirmation,
  };
}
