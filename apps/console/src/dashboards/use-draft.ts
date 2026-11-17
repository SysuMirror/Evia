import { useCallback, useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import {
  api,
  type Dashboard,
  type DashboardDefinition,
  type DashboardDraft,
  type DashboardVersion,
} from '@evia/api-client';
import { useToast } from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import { initialDashboardDefinition, dashboardIssues } from './model';
import {
  revisionConflict,
  dashboardsKey,
  uncertain,
  type CreateAttempt,
  type DashboardWorkspace,
  type ScopedRequest,
} from './types';

type DraftOptions = {
  context: CatalogPageProps;
  identity: string;
  canWrite: boolean;
  busy: string;
  perform: ScopedRequest;
  isPending: () => boolean;
};
/** Editable input and the persisted revision remain distinct through every recovery path. */
export function useDashboardDraft({
  context,
  identity,
  canWrite,
  busy,
  perform,
  isPending,
}: DraftOptions) {
  const { project, environment, session } = context;
  const cache = useQueryClient(),
    toast = useToast();
  const [storedWorkspace, setWorkspace] = useState<DashboardWorkspace>();
  const workspace = storedWorkspace?.scope === identity ? storedWorkspace : undefined;
  const [latest, setLatest] = useState<DashboardDraft>();
  const [createAttempt, setCreateAttempt] = useState<CreateAttempt>();
  const [editorValid, setEditorValid] = useState(true),
    [editorPending, setEditorPending] = useState(false);
  const dirty =
    !!workspace &&
    (!workspace.draft ||
      JSON.stringify(workspace.definition) !== JSON.stringify(workspace.draft.definition) ||
      workspace.folderID !== (workspace.draft.folder_id ?? '') ||
      editorPending);
  const reset = useCallback(() => {
    setWorkspace(undefined);
    setLatest(undefined);
    setCreateAttempt(undefined);
    setEditorValid(true);
    setEditorPending(false);
  }, []);
  useEffect(reset, [identity, reset]);
  function open(analysis: Dashboard, afterLoad: (draft: DashboardDraft) => void) {
    void perform(
      '读取草稿',
      (signal) => api.getDashboardDraft(project.id, analysis.dashboard_id, signal),
      (draft) => {
        setWorkspace({
          scope: identity,
          draft,
          definition: structuredClone(draft.definition),
          folderID: draft.folder_id ?? '',
        });
        setLatest(undefined);
        setCreateAttempt(undefined);
        setEditorPending(false);
        setEditorValid(true);
        afterLoad(draft);
      },
    );
  }
  function create(folderID = '') {
    setWorkspace({ scope: identity, definition: initialDashboardDefinition(), folderID });
    setLatest(undefined);
    setCreateAttempt(undefined);
    setEditorValid(true);
    setEditorPending(false);
  }
  function save(
    publicationPending: boolean,
    invalidateReview: () => void,
    afterCreate: () => void,
  ) {
    if (
      isPending() ||
      busy ||
      !workspace ||
      !canWrite ||
      !editorValid ||
      editorPending ||
      publicationPending ||
      dashboardIssues(workspace.definition).length
    )
      return;
    const queryKey = dashboardsKey(project.id, environment, session.user_id);
    if (!workspace.draft) {
      const next = createAttempt ?? {
        key: crypto.randomUUID(),
        body: {
          environment,
          folder_id: workspace.folderID || undefined,
          definition: structuredClone(workspace.definition),
        },
      };
      setCreateAttempt(next);
      void perform(
        '创建看板',
        (signal) =>
          api.createDashboard(project.id, next.body, next.key, session.csrf_token, signal),
        (draft) => {
          setWorkspace({
            scope: identity,
            draft,
            definition: structuredClone(draft.definition),
            folderID: draft.folder_id ?? '',
          });
          setCreateAttempt(undefined);
          invalidateReview();
          afterCreate();
          void cache.invalidateQueries({ queryKey });
          toast.success('看板草稿已创建');
        },
        (failure) => {
          if (!uncertain(failure)) setCreateAttempt(undefined);
        },
      );
    } else {
      const draft = workspace.draft,
        definition = structuredClone(workspace.definition);
      void perform(
        '保存草稿',
        (signal) =>
          api.updateDashboardDraft(
            project.id,
            draft.dashboard_id,
            draft.draft_revision,
            { definition, folder_id: workspace.folderID || undefined },
            session.csrf_token,
            signal,
          ),
        (saved) => {
          setWorkspace({
            scope: identity,
            draft: saved,
            definition: structuredClone(saved.definition),
            folderID: saved.folder_id ?? '',
          });
          setLatest(undefined);
          invalidateReview();
          void cache.invalidateQueries({ queryKey });
          toast.success('草稿已保存');
        },
        (failure) => {
          if (revisionConflict(failure)) invalidateReview();
        },
      );
    }
  }
  function readLatest(invalidateReview: () => void) {
    if (!workspace?.draft) return;
    void perform(
      '读取最新草稿',
      (signal) => api.getDashboardDraft(project.id, workspace.draft!.dashboard_id, signal),
      (draft) => {
        setLatest(draft);
        invalidateReview();
      },
    );
  }
  function changeDefinition(definition: DashboardDefinition) {
    setWorkspace((previous) => (previous ? { ...previous, definition } : previous));
  }
  function changeFolder(folderID: string) {
    setWorkspace((previous) => (previous ? { ...previous, folderID } : previous));
  }
  function rebaseLocal() {
    if (!latest) return;
    setWorkspace((previous) => (previous ? { ...previous, draft: latest } : previous));
    setLatest(undefined);
  }
  function adoptLatest() {
    if (!latest) return;
    setWorkspace((previous) => ({
      scope: identity,
      draft: latest,
      definition: structuredClone(latest.definition),
      folderID: latest.folder_id ?? previous?.folderID ?? '',
    }));
    setLatest(undefined);
    setEditorPending(false);
    setEditorValid(true);
  }
  function markPublished(published: DashboardVersion) {
    setWorkspace((previous) =>
      previous?.draft
        ? { ...previous, draft: { ...previous.draft, published_version: published.version } }
        : previous,
    );
  }
  function close() {
    setWorkspace(undefined);
    setLatest(undefined);
    setCreateAttempt(undefined);
  }
  return {
    workspace,
    latest,
    createAttempt,
    editorValid,
    editorPending,
    dirty,
    setEditorValid,
    setEditorPending,
    reset,
    open,
    create,
    save,
    readLatest,
    changeDefinition,
    changeFolder,
    rebaseLocal,
    adoptLatest,
    markPublished,
    close,
  };
}
