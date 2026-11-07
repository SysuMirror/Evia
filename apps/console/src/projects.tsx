import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, ApiError, type Project, type ProjectInput, type Session } from '@evia/api-client';
import {
  App,
  Button,
  DataSurface,
  Descriptions,
  EmptyPanel,
  ErrorPanel,
  Form,
  Input,
  LoadingPanel,
  Modal,
  PageHeading,
  PaginationBar,
  Select,
  Table,
  Tag,
  Typography,
  type TableColumnsType,
} from '@evia/design-system';

const zones = [...new Set(['UTC', 'Asia/Shanghai', ...Intl.supportedValuesOf('timeZone')])].map(
  (zone) => ({ value: zone, label: zone }),
);
export function ProjectsPage({ session }: { session: Session }) {
  const cache = useQueryClient();
  const { message, modal } = App.useApp();
  const [cursors, setCursors] = useState<string[]>([]);
  const [creating, setCreating] = useState(false);
  const [confirmingDiscard, setConfirmingDiscard] = useState(false);
  const [selected, setSelected] = useState<Project>();
  const [form] = Form.useForm<ProjectInput>();
  const cursor = cursors.at(-1);
  const projects = useQuery({
    queryKey: ['projects', cursor ?? ''],
    queryFn: ({ signal }) => api.projects(cursor, signal),
  });
  const create = useMutation({
    mutationFn: (body: ProjectInput) => api.createProject(body, session.csrf_token),
    onSuccess: async (project) => {
      setCreating(false);
      form.resetFields();
      setCursors([]);
      await cache.invalidateQueries({ queryKey: ['projects'] });
      void message.success(`已创建项目「${project.name}」`);
    },
  });
  useEffect(() => {
    if (!creating) return;
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (form.isFieldsTouched()) event.preventDefault();
    };
    window.addEventListener('beforeunload', beforeUnload);
    return () => window.removeEventListener('beforeunload', beforeUnload);
  }, [creating, form]);
  const openCreate = () => {
    create.reset();
    setCreating(true);
  };
  const closeCreate = () => {
    if (create.isPending || confirmingDiscard) return;
    if (form.isFieldsTouched()) {
      setConfirmingDiscard(true);
      let discarded = false;
      modal.confirm({
        title: '放弃尚未保存的项目？',
        content: '当前填写的名称和时区将不会保存。',
        okText: '放弃',
        cancelText: '继续编辑',
        mask: { closable: false },
        focusable: { autoFocusButton: 'cancel', focusTriggerAfterClose: false },
        onOk: () => {
          discarded = true;
          form.resetFields();
          setCreating(false);
        },
        afterClose: () => {
          setConfirmingDiscard(false);
          if (!discarded) form.focusField('name');
        },
      });
    } else setCreating(false);
  };
  const columns: TableColumnsType<Project> = [
    {
      title: '项目',
      dataIndex: 'name',
      key: 'name',
      render: (_, project) => (
        <Button type="link" className="project-link" onClick={() => setSelected(project)}>
          <span className="project-name">{project.name}</span>
        </Button>
      ),
    },
    { title: '统计时区', dataIndex: 'timezone', key: 'timezone', width: 200 },
    {
      title: '我的权限',
      key: 'permissions',
      width: 140,
      render: (_, project) => (
        <Tag color={project.permissions.includes('project.write') ? 'blue' : 'default'}>
          {project.permissions.includes('project.write') ? '可编辑' : '只读'}
        </Tag>
      ),
    },
  ];
  return (
    <>
      <PageHeading
        title="项目管理"
        action={
          <>
            <Button
              onClick={() => void projects.refetch()}
              loading={projects.isFetching}
              aria-label="刷新"
              aria-busy={projects.isFetching}
            >
              刷新
            </Button>
            <Button type="primary" onClick={openCreate}>
              创建项目
            </Button>
          </>
        }
      />
      {projects.isError ? (
        <ErrorPanel
          message={projects.error.message}
          requestID={projects.error instanceof ApiError ? projects.error.requestID : undefined}
          retry={() => {
            if (projects.error instanceof ApiError && projects.error.code === 'INVALID_CURSOR')
              setCursors([]);
            else void projects.refetch();
          }}
        />
      ) : projects.isPending ? (
        <LoadingPanel label="正在加载项目" />
      ) : (
        <DataSurface
          label="项目列表"
          footer={
            <PaginationBar
              page={cursors.length + 1}
              count={projects.data.items.length}
              hasNext={!!projects.data.next_cursor}
              busy={projects.isFetching}
              onPrevious={() => setCursors((values) => values.slice(0, -1))}
              onNext={() => {
                if (projects.data.next_cursor)
                  setCursors((values) => [...values, projects.data.next_cursor!]);
              }}
            />
          }
        >
          {projects.data.items.length ? (
            <Table
              columns={columns}
              dataSource={projects.data.items}
              rowKey="id"
              pagination={false}
              scroll={{ x: 560 }}
            />
          ) : (
            <EmptyPanel description={cursors.length ? '这一页没有更多项目' : '暂无项目'} />
          )}
        </DataSurface>
      )}
      <Modal
        title="创建项目"
        open={creating}
        onCancel={closeCreate}
        onOk={() => form.submit()}
        okText="创建"
        cancelText="取消"
        confirmLoading={create.isPending}
        okButtonProps={{ 'aria-label': '创建', 'aria-busy': create.isPending }}
        closable={!create.isPending}
        keyboard={!create.isPending && !confirmingDiscard}
        mask={{ closable: false }}
        cancelButtonProps={{ disabled: create.isPending }}
        afterOpenChange={(open) => {
          if (open) form.focusField('name');
        }}
      >
        {create.error && (
          <div className="inline-notice">
            <ErrorPanel
              message={create.error.message}
              requestID={create.error instanceof ApiError ? create.error.requestID : undefined}
            />
          </div>
        )}
        <Form
          form={form}
          layout="vertical"
          requiredMark={false}
          initialValues={{ timezone: 'Asia/Shanghai' }}
          onFinish={(values) => create.mutate({ ...values, name: values.name.trim() })}
          disabled={create.isPending}
        >
          <Form.Item
            name="name"
            label="项目名称"
            rules={[
              { required: true, whitespace: true, message: '请输入项目名称' },
              { max: 100, message: '名称不能超过 100 个字符' },
            ]}
          >
            <Input placeholder="例如：社区论坛" maxLength={100} showCount />
          </Form.Item>
          <Form.Item
            name="timezone"
            label="统计时区"
            rules={[{ required: true, message: '请选择统计时区' }]}
            extra="用于划分报表中的日期，原始事件时间统一存储为 UTC。"
          >
            <Select showSearch optionFilterProp="label" options={zones} />
          </Form.Item>
        </Form>
      </Modal>
      <Modal
        title="项目详情"
        open={!!selected}
        onCancel={() => setSelected(undefined)}
        footer={<Button onClick={() => setSelected(undefined)}>完成</Button>}
      >
        {selected && (
          <Descriptions
            column={1}
            bordered
            size="small"
            items={[
              { key: 'name', label: '项目名称', children: selected.name },
              {
                key: 'id',
                label: '项目 ID',
                children: <Typography.Text copyable>{selected.id}</Typography.Text>,
              },
              { key: 'zone', label: '统计时区', children: selected.timezone },
              {
                key: 'role',
                label: '我的权限',
                children: selected.permissions.includes('project.write') ? '可编辑项目' : '只读',
              },
              {
                key: 'capability',
                label: '事件接入',
                children: selected.capabilities.length
                  ? selected.capabilities.join('、')
                  : '尚未启用',
              },
            ]}
          />
        )}
      </Modal>
    </>
  );
}
