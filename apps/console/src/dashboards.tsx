import { Button, EmptyPanel, LoadingPanel, PageHeading, Panel, Tabs } from '@evia/design-system';
import type { CatalogPageProps } from './catalog';
import { CatalogError } from './catalog-helpers';
import { DashboardsList } from './dashboards/list';
import { DashboardDraftView } from './dashboards/draft';
import { DashboardDraftRecovery } from './dashboards/draft-recovery';
import { DashboardFolderTree } from './dashboards/folder-tree';
import { DashboardHistory } from './dashboards/history';
import { DashboardPublicationReview } from './dashboards/publication-review';
import { useDashboardsController } from './dashboards/use-controller';
import './dashboards/styles.css';

export function DashboardsPage(props: CatalogPageProps) {
  const { project, environment, session } = props;
  const controller = useDashboardsController(props);
  const { request, draft, history, publication, runWork } = controller;
  const { workspace } = draft;
  if (!controller.canRead)
    return (
      <>
        <PageHeading title="保存的看板" />
        <EmptyPanel description="暂无读取看板的权限。" />
      </>
    );
  if (request.denied)
    return (
      <>
        <PageHeading title="保存的看板" />
        <CatalogError error={request.error} />
      </>
    );
  const draftContent = workspace && (
    <DashboardDraftView
      workspace={workspace}
      folders={controller.folders.items}
      context={props}
      onDenied={request.deny}
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
      onFolderChange={draft.changeFolder}
      onValidityChange={draft.setEditorValid}
      onPendingChange={draft.setEditorPending}
      onSave={controller.save}
      onValidate={publication.validate}
      onReadLatest={controller.readLatest}
      recovery={
        <DashboardDraftRecovery
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
    <DashboardHistory
      project={project}
      environment={environment}
      session={session}
      dashboardID={workspace.draft.dashboard_id}
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
    <div className="dashboard-workspace section-stack">
      <PageHeading
        title={
          workspace
            ? workspace.draft
              ? workspace.definition.title || '看板草稿'
              : '新建看板'
            : '保存的看板'
        }
        summary={
          workspace
            ? '编辑看板结构、固定分析版本并检查已发布结果。'
            : '按文件夹管理可复用看板，打开草稿或创建新的数据视图。'
        }
        action={
          workspace ? (
            <Button disabled={!!request.busy || runWork.busy} onClick={controller.close}>
              返回列表
            </Button>
          ) : controller.canWrite ? (
            <Button variant="primary" onClick={controller.create} disabled={!!request.busy}>
              新建看板
            </Button>
          ) : undefined
        }
      />
      <CatalogError error={request.error} />
      {request.busy === '读取草稿' && <LoadingPanel label="正在读取草稿" />}
      {workspace ? (
        <Panel label="看板工作区" className="dashboard-tabs">
          <Tabs
            label="看板版本"
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
        <div className="dashboard-folder-layout">
          <DashboardFolderTree
            items={controller.folders.items}
            loading={controller.folders.folders.isPending}
            fetching={controller.folders.folders.isFetching}
            error={controller.folders.folders.error}
            canWrite={controller.canWrite}
            busy={!!request.busy}
            scope={history.folderScope}
            onScopeChange={history.changeFolderScope}
            onCreate={controller.folders.createFolder}
            onRename={controller.folders.renameFolder}
            onDelete={controller.folders.deleteFolder}
            onRetry={() => void controller.folders.folders.refetch()}
          />
          <DashboardsList
            items={history.list.data?.items ?? []}
            folders={controller.folders.items}
            context={props}
            sourceEnvironment={environment}
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
            onMove={controller.moveDashboard}
            onCopy={controller.copyDashboard}
            onRetry={() => void history.list.refetch()}
            onPrevious={history.previousList}
            onNext={history.nextList}
          />
        </div>
      )}
      <DashboardPublicationReview
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
