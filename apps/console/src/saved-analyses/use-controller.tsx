import { useCallback, useEffect, useState } from 'react';
import { ApiError, type SavedAnalysis, type SavedAnalysisDefinition } from '@evia/api-client';
import type { CatalogPageProps } from '../catalog';
import type { SavedRunWork } from '../saved-analysis-run';
import { useUnsavedWork } from '../use-unsaved-work';
import { useSavedAnalysisRequest } from './use-scoped-request';
import { useSavedAnalysisDraft } from './use-draft';
import { useSavedAnalysisHistory } from './use-history';
import { useSavedAnalysisPublication } from './use-publication';

export function useSavedAnalysesController(context: CatalogPageProps) {
  const { project, environment, session } = context;
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
  const request = useSavedAnalysisRequest(context, identity, canRead);
  const draft = useSavedAnalysisDraft({
    context,
    identity,
    canWrite,
    busy: request.busy,
    perform: request.perform,
    isPending: request.isPending,
  });
  const history = useSavedAnalysisHistory({
    context,
    identity,
    analysisID: draft.workspace?.draft?.analysis_id,
    tab,
    canRead,
    denied: request.denied,
  });
  const publication = useSavedAnalysisPublication({
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
  function openDraft(analysis: SavedAnalysis) {
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
    guard.requestLeave(() => {
      draft.create();
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
  function changeDefinition(definition: SavedAnalysisDefinition) {
    draft.changeDefinition(definition);
    publication.resetReview();
  }
  function save() {
    draft.save(!!publication.attempt, publication.resetReview, history.firstList);
  }
  function readLatest() {
    draft.readLatest(publication.resetReview);
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
    frozen,
    onRunWork,
    openDraft,
    create,
    close,
    changeTab,
    changeDefinition,
    save,
    readLatest,
    rebaseLocal,
    adoptLatest,
    confirmation: guard.confirmation,
  };
}
