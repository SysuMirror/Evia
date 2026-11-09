import { useCallback, useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { api, type DashboardValidation, type DashboardVersion } from '@evia/api-client';
import { useToast } from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import {
  dashboardsKey,
  uncertain,
  type PublishAttempt,
  type DashboardWorkspace,
  type ScopedRequest,
} from './types';

type PublicationOptions = {
  context: CatalogPageProps;
  identity: string;
  workspace?: DashboardWorkspace;
  dirty: boolean;
  editorValid: boolean;
  editorPending: boolean;
  canWrite: boolean;
  busy: string;
  perform: ScopedRequest;
  isPending: () => boolean;
  onPublished: (version: DashboardVersion) => void;
};
/** A publication retry retains its exact validation counters, payload and key. */
export function useDashboardPublication({
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
  const [validation, setValidation] = useState<DashboardValidation>();
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
        api.validateDashboard(
          project.id,
          draft.dashboard_id,
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
      dashboardID: workspace.draft.dashboard_id,
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
        api.publishDashboard(
          project.id,
          next.dashboardID,
          next.body,
          next.key,
          session.csrf_token,
          signal,
        ),
      (published) => {
        onPublished(published);
        setOpen(false);
        resetReview();
        const prefix = dashboardsKey(project.id, environment, session.user_id);
        cache.setQueryData(
          [...prefix, 'version', published.dashboard_id, published.version],
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
