import { useEffect, useId, useRef, useState } from 'react';
import type { DashboardDefinition, DashboardWidget } from '@evia/api-client';
import {
  Badge,
  Button,
  DashboardCard,
  DashboardGrid,
  Field,
  Input,
  Textarea,
  Modal,
  nextDashboardLayout,
} from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import { DashboardWidgetPicker } from './picker';
import { DashboardParameterEditor } from './parameter-editor';
import { WidgetParameterBindings } from './parameter-bindings';
import {
  availableWidgetPosition,
  dashboardIssues,
  maxTextCardContentRunes,
  reorderWidget,
  widgetSourceLabel,
} from './model';
import { TextCardMarkdown } from './text-card';
import type { DashboardWidgetSource } from './types';

export function DashboardEditor({
  context,
  value,
  disabled,
  readOnly,
  onChange,
  onValidityChange,
  onPendingChange,
  onDenied,
}: {
  context: CatalogPageProps;
  value: DashboardDefinition;
  disabled: boolean;
  readOnly: boolean;
  onChange: (value: DashboardDefinition) => void;
  onValidityChange: (valid: boolean) => void;
  onPendingChange: (pending: boolean) => void;
  onDenied: (error: Error) => void;
}) {
  const id = useId(),
    [picker, setPicker] = useState(false),
    [editing, setEditing] = useState<string>(),
    [replacing, setReplacing] = useState<string>(),
    [notice, setNotice] = useState('');
  const fallback = useRef<HTMLButtonElement>(null),
    returnTarget = useRef<HTMLButtonElement | null>(null);
  const [title, setTitle] = useState('');
  const issues = dashboardIssues(value),
    widget = value.widgets.find((w) => w.widget_id === editing),
    frozen = disabled || readOnly;
  useEffect(
    () => onValidityChange(issues.length === 0),
    [JSON.stringify(issues), onValidityChange],
  );
  useEffect(() => onPendingChange(false), [onPendingChange]);
  function update(next: DashboardWidget) {
    onChange({
      ...value,
      widgets: value.widgets.map((w) => (w.widget_id === next.widget_id ? next : w)),
    });
  }
  function layout(patch: Partial<DashboardWidget['layout']>) {
    if (!widget || frozen) return;
    const next = nextDashboardLayout(
      widget.layout,
      value.widgets.filter((w) => w.widget_id !== widget.widget_id).map((w) => w.layout),
      patch,
    );
    if (!next) {
      setNotice('该位置与其他组件重叠或超出布局边界。');
      return;
    }
    update({ ...widget, layout: next });
    setNotice(
      `布局已调整，第 ${next.y + 1} 行，第 ${next.x + 1} 列，宽 ${next.width} 列，高 ${next.height} 行。`,
    );
  }
  function add(source: DashboardWidgetSource, title: string) {
    const pinned =
      source.kind === 'analysis'
        ? {
            analysis_id: source.analysis.analysis_id,
            analysis_version: source.analysis.version,
          }
        : source.kind === 'metric'
          ? {
              metric_ref: {
                metric_id: source.version.metric_id,
                version: source.version.version,
              },
            }
          : source.kind === 'formula'
            ? {
                metric_ref: {
                  metric_id: source.version.metric_id,
                  version: source.version.version,
                  kind: 'composite' as const,
                },
              }
            : { text_card: { content: source.content } };
    if (replacing) {
      const current = value.widgets.find((w) => w.widget_id === replacing);
      if (current) {
        // Switching source kind replaces the old reference entirely; layout and
        // identity are preserved, and exactly one source remains on the widget.
        // A fresh object also drops parameter bindings that only executable
        // sources may carry.
        const next: DashboardWidget = {
          widget_id: current.widget_id,
          title,
          reading_order: current.reading_order,
          layout: current.layout,
          ...pinned,
        };
        update(next);
      }
      setReplacing(undefined);
      return;
    }
    const w: DashboardWidget = {
      widget_id: crypto.randomUUID(),
      title,
      reading_order: value.widgets.length,
      layout: availableWidgetPosition(value.widgets),
      ...pinned,
    };
    onChange({ ...value, widgets: [...value.widgets, w] });
    setNotice(`已添加组件 ${title}`);
  }
  return (
    <div className="section-stack dashboard-editor">
      <div className="dashboard-metadata">
        <Field label="看板标题" htmlFor={`${id}-title`}>
          <Input
            id={`${id}-title`}
            required
            maxLength={256}
            value={value.title}
            readOnly={readOnly}
            disabled={disabled}
            onChange={(e) => onChange({ ...value, title: e.target.value })}
          />
        </Field>
        <Field label="描述" htmlFor={`${id}-description`}>
          <Textarea
            id={`${id}-description`}
            rows={2}
            maxLength={4000}
            value={value.description ?? ''}
            readOnly={readOnly}
            disabled={disabled}
            onChange={(e) => onChange({ ...value, description: e.target.value })}
          />
        </Field>
      </div>
      <DashboardParameterEditor value={value} frozen={frozen} onChange={onChange} />
      <div className="dashboard-section-actions">
        <span>{value.widgets.length} / 12 个组件</span>
        <Button
          ref={fallback}
          disabled={frozen || value.widgets.length >= 12}
          onClick={() => {
            setReplacing(undefined);
            setPicker(true);
          }}
        >
          添加组件
        </Button>
      </div>
      {issues.length > 0 && (
        <ul className="field-help" aria-label="待完善看板">
          {issues.map((issue) => (
            <li key={issue}>{issue}</li>
          ))}
        </ul>
      )}
      <p role="status" aria-live="polite" className="field-help">
        {notice}
      </p>
      {value.widgets.length === 0 ? (
        <div className="dashboard-empty">
          <h3>添加第一个组件</h3>
          <p>
            选择已发布分析、独立指标的精确版本或 Markdown
            文本卡，再设置布局。可并排展示趋势、指标、说明与完整结果表。
          </p>
        </div>
      ) : (
        <DashboardGrid
          label="草稿组件布局"
          items={value.widgets.map((w) => ({
            id: w.widget_id,
            reading_order: w.reading_order,
            layout: w.layout,
            content: (
              <DashboardCard
                title={w.title}
                summary={`${widgetSourceLabel(w)}，阅读顺序 ${w.reading_order + 1}`}
                action={
                  <Button
                    disabled={frozen}
                    onClick={(e) => {
                      returnTarget.current = e.currentTarget;
                      setTitle(w.title);
                      setEditing(w.widget_id);
                      setNotice('');
                    }}
                    aria-label={`编辑组件 ${w.title}`}
                  >
                    编辑组件
                  </Button>
                }
              >
                <div className="dashboard-widget-preview">
                  <span>{widgetSourceLabel(w)}</span>
                  <dl>
                    <div>
                      <dt>位置</dt>
                      <dd>
                        第 {w.layout.y + 1} 行，第 {w.layout.x + 1} 列
                      </dd>
                    </div>
                    <div>
                      <dt>尺寸</dt>
                      <dd>
                        {w.layout.width} 列 × {w.layout.height} 行
                      </dd>
                    </div>
                  </dl>
                  {w.text_card ? (
                    <p>文本卡仅展示 Markdown 说明，不产生查询。</p>
                  ) : (
                    <p>发布后可按统一时间运行真实查询。</p>
                  )}
                </div>
              </DashboardCard>
            ),
          }))}
        />
      )}
      <DashboardWidgetPicker
        context={context}
        open={picker}
        onClose={() => setPicker(false)}
        onAdd={add}
        onDenied={onDenied}
      />
      <Modal
        open={!!widget}
        title="编辑组件"
        onRequestClose={() => {
          if (!disabled) {
            setEditing(undefined);
            onPendingChange(false);
          }
        }}
        footer={
          <Button
            variant="primary"
            disabled={disabled}
            onClick={() => {
              setEditing(undefined);
              onPendingChange(false);
            }}
          >
            完成
          </Button>
        }
      >
        {widget && (
          <div className="section-stack">
            <Field label="组件标题" htmlFor={`${id}-widget-title`}>
              <Input
                id={`${id}-widget-title`}
                value={widget.title}
                maxLength={256}
                required
                disabled={frozen}
                onChange={(e) => update({ ...widget, title: e.target.value })}
              />
            </Field>
            {widget.text_card ? (
              <>
                <Field label="文本卡内容（Markdown 子集）" htmlFor={`${id}-text-content`}>
                  <Textarea
                    id={`${id}-text-content`}
                    rows={8}
                    maxLength={maxTextCardContentRunes}
                    required
                    disabled={frozen}
                    value={widget.text_card.content}
                    onChange={(e) => update({ ...widget, text_card: { content: e.target.value } })}
                  />
                </Field>
                <p className="field-help">
                  {[...widget.text_card.content].length}/{maxTextCardContentRunes} 字符；不支持原始
                  HTML、图片与非 http(s) 链接。
                </p>
                <div className="text-card-preview">
                  <p className="field-help">预览</p>
                  <TextCardMarkdown content={widget.text_card.content} />
                </div>
              </>
            ) : (
              <>
                <Button
                  disabled={frozen}
                  onClick={() => {
                    setReplacing(widget.widget_id);
                    setPicker(true);
                  }}
                >
                  更换组件来源
                </Button>
                <WidgetParameterBindings
                  context={context}
                  widget={widget}
                  parameters={value.parameters ?? []}
                  value={widget.parameter_bindings ?? []}
                  disabled={frozen}
                  onChange={(parameter_bindings) =>
                    update({
                      ...widget,
                      parameter_bindings: parameter_bindings.length
                        ? parameter_bindings
                        : undefined,
                    })
                  }
                />
              </>
            )}
            <p className="field-help">
              方向按钮移动一格，尺寸按钮调整一格。位置和尺寸立即应用，碰撞与越界时保留原布局。
            </p>
            <fieldset disabled={frozen} className="dashboard-layout-controls">
              <legend>移动组件</legend>
              <Button onClick={() => layout({ y: widget.layout.y - 1 })}>向上移动</Button>
              <Button onClick={() => layout({ y: widget.layout.y + 1 })}>向下移动</Button>
              <Button onClick={() => layout({ x: widget.layout.x - 1 })}>向左移动</Button>
              <Button onClick={() => layout({ x: widget.layout.x + 1 })}>向右移动</Button>
            </fieldset>
            <fieldset disabled={frozen} className="dashboard-layout-controls">
              <legend>调整尺寸</legend>
              <Button onClick={() => layout({ width: widget.layout.width - 1 })}>减少宽度</Button>
              <Button onClick={() => layout({ width: widget.layout.width + 1 })}>增加宽度</Button>
              <Button onClick={() => layout({ height: widget.layout.height - 1 })}>减少高度</Button>
              <Button onClick={() => layout({ height: widget.layout.height + 1 })}>增加高度</Button>
            </fieldset>
            <p role="status" aria-live="polite">
              {notice ||
                `第 ${widget.layout.y + 1} 行，第 ${widget.layout.x + 1} 列，宽 ${widget.layout.width} 列，高 ${widget.layout.height} 行。`}
            </p>
            <fieldset disabled={frozen} className="dashboard-layout-controls">
              <legend>阅读顺序 {widget.reading_order + 1}</legend>
              <Button
                disabled={frozen || widget.reading_order === 0}
                onClick={() =>
                  onChange({
                    ...value,
                    widgets: reorderWidget(value.widgets, widget.widget_id, -1),
                  })
                }
              >
                提前阅读
              </Button>
              <Button
                disabled={frozen || widget.reading_order === value.widgets.length - 1}
                onClick={() =>
                  onChange({ ...value, widgets: reorderWidget(value.widgets, widget.widget_id, 1) })
                }
              >
                延后阅读
              </Button>
            </fieldset>
            <Button
              variant="danger"
              disabled={frozen}
              onClick={() => {
                onChange({
                  ...value,
                  widgets: value.widgets
                    .filter((w) => w.widget_id !== widget.widget_id)
                    .sort((a, b) => a.reading_order - b.reading_order)
                    .map((w, reading_order) => ({ ...w, reading_order })),
                });
                setEditing(undefined);
                onPendingChange(false);
              }}
            >
              移除组件
            </Button>
          </div>
        )}
      </Modal>
    </div>
  );
}
