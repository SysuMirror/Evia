import type { SavedAnalysisTemplate } from '@evia/api-client';
import { Button, Field, Input, Select, Textarea } from '@evia/design-system';
import {
  flatSavedFilters,
  savedVisualizationLabels,
  type SavedVisualization,
} from '../saved-analysis-model';
import type { EditorSection, SectionControlProps } from './types';

type MetadataFieldsProps = SectionControlProps & {
  title: string;
  description: string;
  kind: SavedAnalysisTemplate['kind'];
  visualization: SavedVisualization;
  visualizationError?: string;
  metricMode?: boolean;
  onTitleChange: (next: string) => void;
  onDescriptionChange: (next: string) => void;
  onKindChange: (next: SavedAnalysisTemplate['kind']) => void;
  onVisualizationChange: (next: SavedVisualization) => void;
};
export function MetadataFields({
  id,
  title,
  description,
  kind,
  visualization,
  visualizationError,
  metricMode = false,
  onTitleChange,
  onDescriptionChange,
  onKindChange,
  onVisualizationChange,
  readOnly,
  disabled,
}: MetadataFieldsProps) {
  return (
    <div className="field-grid">
      <Field label="分析标题" htmlFor={`${id}-title`} required>
        <Input
          id={`${id}-title`}
          required
          maxLength={128}
          readOnly={readOnly}
          disabled={disabled}
          value={title}
          onChange={(event) => onTitleChange(event.target.value)}
        />
      </Field>
      <Field label="描述" htmlFor={`${id}-description`}>
        <Textarea
          id={`${id}-description`}
          rows={2}
          maxLength={2000}
          readOnly={readOnly}
          disabled={disabled}
          value={description}
          onChange={(event) => onDescriptionChange(event.target.value)}
        />
      </Field>
      {!metricMode && (
        <Field label="分析类型" htmlFor={`${id}-kind`}>
          <Select
            id={`${id}-kind`}
            value={kind}
            disabled={readOnly || disabled}
            options={[
              { value: 'trend', label: '趋势分析' },
              { value: 'funnel', label: '有序漏斗' },
              { value: 'retention', label: '留存分析' },
            ]}
            onChange={(event) => onKindChange(event.target.value as SavedAnalysisTemplate['kind'])}
          />
        </Field>
      )}
      <Field label="展示方式" htmlFor={`${id}-visualization`} error={visualizationError}>
        <Select
          id={`${id}-visualization`}
          disabled={readOnly || disabled || metricMode}
          value={visualization}
          options={Object.entries(savedVisualizationLabels).map(([value, label]) => ({
            value,
            label,
          }))}
          onChange={(event) => onVisualizationChange(event.target.value as SavedVisualization)}
        />
      </Field>
    </div>
  );
}

export function ConfigurationSummary({
  query,
  disabled,
  onOpen,
}: {
  query: SavedAnalysisTemplate;
  disabled: boolean;
  onOpen: (section: EditorSection) => void;
}) {
  return (
    <div
      className="saved-analysis-editor-summary inline-actions"
      role="group"
      aria-label="分析配置"
    >
      {query.kind === 'trend' ? (
        <>
          <Button disabled={disabled} onClick={() => onOpen('events')}>
            事件版本 ({query.event_versions?.length ?? 0})
          </Button>
          <Button disabled={disabled} onClick={() => onOpen('metrics')}>
            指标 ({query.metrics?.length || 1})
          </Button>
          <Button disabled={disabled} onClick={() => onOpen('grouping')}>
            分组与粒度
          </Button>
        </>
      ) : query.kind === 'retention' ? (
        <Button disabled={disabled} onClick={() => onOpen('retention')}>
          留存配置（{query.retention?.periods ?? 0} 期）
        </Button>
      ) : (
        <Button disabled={disabled} onClick={() => onOpen('funnel')}>
          漏斗步骤 ({query.funnel?.steps.length ?? 0})
        </Button>
      )}
      <Button disabled={disabled} onClick={() => onOpen('filters')}>
        筛选
        {flatSavedFilters(query.filter) === null
          ? '（高级）'
          : ` (${flatSavedFilters(query.filter)?.rows.length ?? 0})`}
      </Button>
      <Button disabled={disabled} onClick={() => onOpen('json')}>
        高级 JSON
      </Button>
    </div>
  );
}
