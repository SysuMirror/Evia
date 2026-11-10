import { useCallback, useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import {
  api,
  type SavedAnalysis,
  type SavedAnalysisDefinition,
  type SavedAnalysisDraft,
  type SavedAnalysisVersion,
} from '@evia/api-client';
import { useToast } from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import {
  initialSavedAnalysisDefinition,
  savedAnalysisIssues,
  toSavedAnalysisInput,
} from '../saved-analysis-model';
import {
  revisionConflict,
  savedAnalysesKey,
  uncertain,
  type CreateAttempt,
  type SavedAnalysisWorkspace,
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
export function useSavedAnalysisDraft({
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
  const [storedWorkspace, setWorkspace] = useState<SavedAnalysisWorkspace>();
  const workspace = storedWorkspace?.scope === identity ? storedWorkspace : undefined;
  const [latest, setLatest] = useState<SavedAnalysisDraft>();
  const [createAttempt, setCreateAttempt] = useState<CreateAttempt>();
  const [editorValid, setEditorValid] = useState(true),
    [editorPending, setEditorPending] = useState(false);
  const dirty =
    !!workspace &&
    (!workspace.draft ||
      JSON.stringify(workspace.definition) !== JSON.stringify(workspace.draft.definition) ||
      editorPending);
  const reset = useCallback(() => {
    setWorkspace(undefined);
    setLatest(undefined);
    setCreateAttempt(undefined);
    setEditorValid(true);
    setEditorPending(false);
  }, []);
  useEffect(reset, [identity, reset]);
  function open(analysis: SavedAnalysis, afterLoad: (draft: SavedAnalysisDraft) => void) {
    void perform(
      '读取草稿',
      (signal) => api.getSavedAnalysisDraft(project.id, analysis.analysis_id, signal),
      (draft) => {
        setWorkspace({ scope: identity, draft, definition: structuredClone(draft.definition) });
        setLatest(undefined);
        setCreateAttempt(undefined);
        setEditorPending(false);
        setEditorValid(true);
        afterLoad(draft);
      },
    );
  }
  function create() {
    setWorkspace({ scope: identity, definition: initialSavedAnalysisDefinition() });
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
      savedAnalysisIssues(workspace.definition).length
    )
      return;
    const queryKey = savedAnalysesKey(project.id, environment, session.user_id);
    if (!workspace.draft) {
      const next = createAttempt ?? {
        key: crypto.randomUUID(),
        body: { environment, definition: toSavedAnalysisInput(workspace.definition) },
      };
      setCreateAttempt(next);
      void perform(
        '创建分析',
        (signal) =>
          api.createSavedAnalysis(project.id, next.body, next.key, session.csrf_token, signal),
        (draft) => {
          setWorkspace({ scope: identity, draft, definition: structuredClone(draft.definition) });
          setCreateAttempt(undefined);
          invalidateReview();
          afterCreate();
          void cache.invalidateQueries({ queryKey });
          toast.success('分析草稿已创建');
        },
        (failure) => {
          if (!uncertain(failure)) setCreateAttempt(undefined);
        },
      );
    } else {
      const draft = workspace.draft,
        definition = toSavedAnalysisInput(workspace.definition);
      void perform(
        '保存草稿',
        (signal) =>
          api.updateSavedAnalysisDraft(
            project.id,
            draft.analysis_id,
            draft.draft_revision,
            { definition },
            session.csrf_token,
            signal,
          ),
        (saved) => {
          setWorkspace({
            scope: identity,
            draft: saved,
            definition: structuredClone(saved.definition),
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
      (signal) => api.getSavedAnalysisDraft(project.id, workspace.draft!.analysis_id, signal),
      (draft) => {
        setLatest(draft);
        invalidateReview();
      },
    );
  }
  function changeDefinition(definition: SavedAnalysisDefinition) {
    setWorkspace((previous) => (previous ? { ...previous, definition } : previous));
  }
  function rebaseLocal() {
    if (!latest) return;
    setWorkspace((previous) => (previous ? { ...previous, draft: latest } : previous));
    setLatest(undefined);
  }
  function adoptLatest() {
    if (!latest) return;
    setWorkspace({
      scope: identity,
      draft: latest,
      definition: structuredClone(latest.definition),
    });
    setLatest(undefined);
    setEditorPending(false);
    setEditorValid(true);
  }
  function markPublished(published: SavedAnalysisVersion) {
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
    rebaseLocal,
    adoptLatest,
    markPublished,
    close,
  };
}
