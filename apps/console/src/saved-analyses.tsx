import { Button, EmptyPanel, LoadingPanel, PageHeading, Panel, Tabs } from '@evia/design-system';
import type { CatalogPageProps } from './catalog';
import { CatalogError } from './catalog-helpers';
import { SavedAnalysesList } from './saved-analyses/list';
import { SavedAnalysisDraftView } from './saved-analyses/draft';
import { SavedAnalysisDraftRecovery } from './saved-analyses/draft-recovery';
import { SavedAnalysisHistory } from './saved-analyses/history';
import { SavedAnalysisPublicationReview } from './saved-analyses/publication-review';
import { useSavedAnalysesController } from './saved-analyses/use-controller';

export function SavedAnalysesPage(props: CatalogPageProps) {
  const { project, environment, session } = props;
  const controller = useSavedAnalysesController(props);
  const { request, draft, history, publication, runWork } = controller;
  const { workspace } = draft;
  if (!controller.canRead)
    return (
      <>
        <PageHeading title="保存的分析" />
        <EmptyPanel description="暂无读取分析的权限。" />
      </>
    );
  if (request.denied)
    return (
      <>
        <PageHeading title="保存的分析" />
        <CatalogError error={request.error} />
      </>
    );
  const draftContent = workspace && (
    <SavedAnalysisDraftView
      workspace={workspace}
      projectID={project.id}
      actorID={session.user_id}
      environment={environment}
      canWrite={controller.canWrite}
      busy={request.busy}
      frozen={controller.frozen}
      dirty={draft.dirty}
      editorValid={draft.editorValid}
      editorPending={draft.editorPending}
      editorRevision={draft.latest && !draft.dirty ? draft.latest.draft_revision : ''}
      publicationPending={!!publication.attempt}
      onDefinitionChange={controller.changeDefinition}
      onValidityChange={draft.setEditorValid}
      onPendingChange={draft.setEditorPending}
      onSave={controller.save}
      onValidate={publication.validate}
      onReadLatest={controller.readLatest}
      recovery={
        <SavedAnalysisDraftRecovery
          latest={draft.latest}
          error={request.error}
          busy={!!request.busy}
          frozen={controller.frozen}
          creationPending={!!draft.createAttempt}
          publicationPending={!!publication.attempt}
          reviewOpen={publication.open}
          onSave={controller.save}
          onReadLatest={controller.readLatest}
          onResumePublication={() => publication.setOpen(true)}
          onRebaseLocal={controller.rebaseLocal}
          onAdoptLatest={controller.adoptLatest}
        />
      }
    />
  );
  const publishedContent = workspace?.draft && (
    <SavedAnalysisHistory
      project={project}
      environment={environment}
      session={session}
      analysisID={workspace.draft.analysis_id}
      publishedVersion={workspace.draft.published_version}
      versionNumber={history.versionNumber}
      displayVersion={history.displayVersion}
      options={history.options}
      busy={request.busy}
      runWork={runWork}
      page={history.versionPage}
      count={history.versions.data?.items.length ?? 0}
      hasNext={!!history.versions.data?.next_cursor}
      versionsFetching={history.versions.isFetching}
      versionsError={history.versions.error}
      selectedPending={history.selectedVersion.isPending}
      selectedFetching={history.selectedVersion.isFetching}
      selectedError={history.selectedVersion.error}
      onSelectVersion={history.setVersionNumber}
      onRetryVersion={() => void history.selectedVersion.refetch()}
      onPrevious={history.previousVersion}
      onNext={history.nextVersion}
      onRunWork={controller.onRunWork}
      onAccessDenied={request.deny}
    />
  );
  return (
    <div className="saved-analysis-workspace section-stack">
      <PageHeading
        title={
          workspace
            ? workspace.draft
              ? workspace.definition.title || '分析草稿'
              : '新建分析'
            : '保存的分析'
        }
        action={
          workspace ? (
            <Button disabled={!!request.busy || runWork.busy} onClick={controller.close}>
              返回列表
            </Button>
          ) : controller.canWrite ? (
            <Button variant="primary" onClick={controller.create} disabled={!!request.busy}>
              新建分析
            </Button>
          ) : undefined
        }
      />
      <CatalogError error={request.error} />
      {request.busy === '读取草稿' && <LoadingPanel label="正在读取草稿" />}
      {workspace ? (
        <Panel label="分析工作区" className="saved-analysis-tabs">
          <Tabs
            label="分析版本"
            value={controller.tab}
            onChange={controller.changeTab}
            items={[
              {
                id: 'draft',
                label: '草稿',
                content: controller.tab === 'draft' ? draftContent : null,
              },
              {
                id: 'versions',
                label: '已发布版本',
                disabled: !workspace.draft,
                content: controller.tab === 'versions' ? publishedContent : null,
              },
            ]}
          />
        </Panel>
      ) : (
        <SavedAnalysesList
          items={history.list.data?.items ?? []}
          error={history.list.error}
          loading={history.list.isPending}
          fetching={history.list.isFetching}
          busy={!!request.busy}
          busyLabel={request.busy}
          canWrite={controller.canWrite}
          timezone={project.timezone}
          page={history.listPage}
          hasNext={!!history.list.data?.next_cursor}
          view={history.archiveView}
          onViewChange={history.changeView}
          search={history.searchInput}
          onSearchChange={history.changeSearch}
          onOpen={controller.openDraft}
          onArchive={controller.archive}
          onUnarchive={controller.unarchive}
          onRetry={() => void history.list.refetch()}
          onPrevious={history.previousList}
          onNext={history.nextList}
        />
      )}
      <SavedAnalysisPublicationReview
        open={publication.open}
        validation={publication.validation}
        busy={request.busy}
        canWrite={controller.canWrite}
        acknowledged={publication.acknowledged}
        dirty={draft.dirty}
        retryPending={!!publication.attempt}
        error={request.error}
        onClose={() => publication.setOpen(false)}
        onPublish={publication.publish}
        onAcknowledge={publication.setAcknowledged}
      />
      {controller.confirmation}
    </div>
  );
}
