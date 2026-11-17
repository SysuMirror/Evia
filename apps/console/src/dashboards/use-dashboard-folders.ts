import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, type DashboardFolder } from '@evia/api-client';
import type { CatalogPageProps } from '../catalog';
import { dashboardsKey, type ScopedRequest } from './types';

type FolderHookOptions = {
  context: CatalogPageProps;
  canRead: boolean;
  denied: boolean;
  perform: ScopedRequest;
};

/**
 * Folder metadata is small and bounded (depth <= 3), so the console keeps one
 * paged query per scope and derives the tree client-side. Mutations reuse the
 * shared request slot so 401/403 handling and busy state stay consistent.
 */
export function useDashboardFolders({ context, canRead, denied, perform }: FolderHookOptions) {
  const { project, environment, session } = context;
  const prefix = dashboardsKey(project.id, environment, session.user_id);
  const cache = useQueryClient();
  const folders = useQuery({
    queryKey: [...prefix, 'folders'],
    queryFn: ({ signal }) =>
      api.listDashboardFolders(project.id, { environment, limit: 100 }, signal),
    enabled: canRead && !denied,
  });

  function invalidate() {
    void cache.invalidateQueries({ queryKey: prefix });
  }

  function createFolder(input: { title: string; parentID: string }, done: () => void) {
    void perform(
      '新建文件夹',
      (signal) =>
        api.createDashboardFolder(
          project.id,
          { environment, title: input.title, parent_id: input.parentID || undefined },
          crypto.randomUUID(),
          session.csrf_token,
          signal,
        ),
      () => {
        invalidate();
        done();
      },
    );
  }

  function renameFolder(folder: DashboardFolder, title: string, done: () => void) {
    void perform(
      '重命名文件夹',
      (signal) =>
        api.updateDashboardFolder(
          project.id,
          folder.folder_id,
          folder.revision,
          { title },
          session.csrf_token,
          signal,
        ),
      () => {
        invalidate();
        done();
      },
    );
  }

  function deleteFolder(folder: DashboardFolder, done: () => void) {
    void perform(
      '删除文件夹',
      (signal) =>
        api.deleteDashboardFolder(project.id, folder.folder_id, session.csrf_token, signal),
      () => {
        invalidate();
        done();
      },
    );
  }

  const items = folders.data?.items ?? [];
  return { folders, items, createFolder, renameFolder, deleteFolder, invalidate };
}

/** Folders that may accept a child: depth 1 and 2 only (the cap is depth 3). */
export function eligibleParents(items: DashboardFolder[]): DashboardFolder[] {
  return items.filter((folder) => folder.depth < 3);
}
