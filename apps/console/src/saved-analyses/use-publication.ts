import { useCallback, useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { api, type SavedAnalysisValidation, type SavedAnalysisVersion } from '@evia/api-client';
import { useToast } from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import {
  savedAnalysesKey,
  uncertain,
  type PublishAttempt,
  type SavedAnalysisWorkspace,
  type ScopedRequest,
} from './types';

type PublicationOptions = {
  context: CatalogPageProps;
  identity: string;
  workspace?: SavedAnalysisWorkspace;
  dirty: boolean;
  editorValid: boolean;
  editorPending: boolean;
  canWrite: boolean;
  busy: string;
  perform: ScopedRequest;
  isPending: () => boolean;
  onPublished: (version: SavedAnalysisVersion) => void;
};
/** A publication retry retains its exact validation counters, payload and key. */
export function useSavedAnalysisPublication({
  context,
  identity,
  workspace,
  dirty,
  editorValid,
  editorPending,
  canWrite,
  busy,
  perform,
  isPending,
  onPublished,
}: PublicationOptions) {
  const { project, environment, session } = context;
  const cache = useQueryClient(),
    toast = useToast();
  const [validation, setValidation] = useState<SavedAnalysisValidation>();
  const [open, setOpen] = useState(false),
    [acknowledged, setAcknowledged] = useState(false);
  const [attempt, setAttempt] = useState<PublishAttempt>();
  const resetReview = useCallback(() => {
    setValidation(undefined);
    setAcknowledged(false);
    setAttempt(undefined);
  }, []);
  const reset = useCallback(() => {
    resetReview();
    setOpen(false);
  }, [resetReview]);
  useEffect(reset, [identity, reset]);
  function validate() {
    if (!workspace?.draft || dirty || editorPending || !editorValid || attempt) return;
    const draft = workspace.draft;
    void perform(
      '校验已保存草稿',
      (signal) =>
        api.validateSavedAnalysis(
          project.id,
          draft.analysis_id,
          {
            draft_revision: draft.draft_revision,
            expected_published_version: draft.published_version,
          },
          session.csrf_token,
          signal,
        ),
      (checked) => {
        setValidation(checked);
        setOpen(true);
        setAcknowledged(false);
      },
    );
  }
  function publish() {
    if (
      isPending() ||
      busy ||
      !workspace?.draft ||
      !validation ||
      !canWrite ||
      !acknowledged ||
      dirty
    )
      return;
    const next = attempt ?? {
      key: crypto.randomUUID(),
      analysisID: workspace.draft.analysis_id,
      body: {
        draft_revision: validation.draft_revision,
        expected_published_version: validation.published_version,
        validation_hash: validation.validation_hash,
      },
    };
    setAttempt(next);
    void perform(
      '发布版本',
      (signal) =>
        api.publishSavedAnalysis(
          project.id,
          next.analysisID,
          next.body,
          next.key,
          session.csrf_token,
          signal,
        ),
      (published) => {
        onPublished(published);
        setOpen(false);
        resetReview();
        const prefix = savedAnalysesKey(project.id, environment, session.user_id);
        cache.setQueryData(
          [...prefix, 'version', published.analysis_id, published.version],
          published,
        );
        void cache.invalidateQueries({ queryKey: prefix });
        toast.success(`已发布不可变版本 v${published.version}`);
      },
      (failure) => {
        if (!uncertain(failure)) resetReview();
      },
    );
  }
  return {
    validation,
    open,
    acknowledged,
    attempt,
    setOpen,
    setAcknowledged,
    resetReview,
    reset,
    validate,
    publish,
  };
}
