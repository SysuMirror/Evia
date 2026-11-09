import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { type Environment, type Project, type Session } from '@evia/api-client';
import { ErrorPanel, PageHeading, Tabs } from '@evia/design-system';
import { useGovernanceRequest } from './use-governance-request';
import { SettingsPanel } from './settings-panel';
import { MembersPanel } from './members-panel';
import { AuditPanel } from './audit-panel';
import './styles.css';

type PageProps = { project: Project; session: Session; environment: Environment };

export function GovernancePage({ project, session }: PageProps) {
  const cache = useQueryClient();
  const canReadSettings = project.permissions.includes('settings.read');
  const canWriteSettings = project.permissions.includes('settings.write');
  const canManageMembers = project.permissions.includes('members.manage');
  const canReadAudit = project.permissions.includes('audit.read');

  // Settings writes, membership changes and audit reads are owner-scoped; the
  // same request slot serializes the write panels and owns denial handling.
  const requests = useGovernanceRequest(
    project,
    session,
    `${project.id}:${session.user_id}:${session.csrf_token}`,
    canWriteSettings || canManageMembers,
  );

  if (!canReadSettings) return <ErrorPanel message="你没有查看该项目治理信息的权限。" />;
  if (requests.denied) return <ErrorPanel message={requests.denied.message} />;

  const refreshGovernance = () => cache.invalidateQueries({ queryKey: ['governance', project.id] });

  const [activeTab, setActiveTab] = useState('settings');

  const tabs = [
    {
      id: 'settings',
      label: '项目设置',
      content: (
        <SettingsPanel
          project={project}
          session={session}
          canEdit={canWriteSettings}
          perform={requests.perform}
          busy={requests.busy}
          revisionConflict={refreshGovernance}
        />
      ),
    },
    ...(canManageMembers
      ? [
          {
            id: 'members',
            label: '成员管理',
            content: (
              <MembersPanel
                project={project}
                session={session}
                perform={requests.perform}
                busy={requests.busy}
              />
            ),
          },
        ]
      : []),
    ...(canReadAudit
      ? [
          {
            id: 'audit',
            label: '审计记录',
            content: <AuditPanel project={project} session={session} />,
          },
        ]
      : []),
  ];

  return (
    <>
      <PageHeading title="项目治理" />
      <Tabs label="项目治理分区" items={tabs} value={activeTab} onChange={setActiveTab} />
    </>
  );
}
