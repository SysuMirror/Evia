import { useId } from 'react';
import { Button } from '@evia/design-system';
import type { Environment, SavedAnalysisDefinition } from '@evia/api-client';
import {
  isMetricReferenceDefinition,
  patchMetricReference,
  savedDefinitionSummary,
  switchSavedAnalysisSource,
  type SavedAnalysisSource,
} from './saved-analysis-model';
import { AdvancedSection } from './saved-analysis-editor/advanced';
import { ConfigurationSummary, MetadataFields } from './saved-analysis-editor/configuration-fields';
import { EditorConfirmations, SectionDialog } from './saved-analysis-editor/editor-dialogs';
import { EventsSection } from './saved-analysis-editor/events';
import { FiltersSection } from './saved-analysis-editor/filters';
import { FunnelSection } from './saved-analysis-editor/funnel';
import { GroupingSection } from './saved-analysis-editor/grouping';
import { MetricReferenceSection } from './saved-analysis-editor/metric-reference';
import { MetricsSection } from './saved-analysis-editor/metrics';
import { RetentionSection } from './saved-analysis-editor/retention';
import { CatalogNotice, useEditorCatalog } from './saved-analysis-editor/use-editor-catalog';
import { useSectionEditor } from './saved-analysis-editor/use-section-editor';
import { useSectionFocus } from './saved-analysis-editor/use-section-focus';

export type SavedAnalysisEditorProps = {
  value: SavedAnalysisDefinition;
  onChange: (next: SavedAnalysisDefinition) => void;
  projectID: string;
  environment: Environment;
  actorID?: string;
  readOnly?: boolean;
  disabled?: boolean;
  onValidityChange?: (valid: boolean) => void;
  onPendingChange?: (pending: boolean) => void;
};

export function SavedAnalysisEditor({
  value,
  onChange,
  projectID,
  environment,
  actorID,
  readOnly = false,
  disabled = false,
  onValidityChange,
  onPendingChange,
}: SavedAnalysisEditorProps) {
  const id = useId();
  const edit = useSectionEditor({
    value,
    onChange,
    readOnly,
    disabled,
    onValidityChange,
    onPendingChange,
  });
  const focus = useSectionFocus(edit.candidate);
  const catalog = useEditorCatalog({
    projectID,
    environment,
    actorID,
    query: value.query,
    section: edit.section,
    readOnly,
  });
  const query = edit.candidate.query;
  const metricMode = isMetricReferenceDefinition(value);
  const metricRef = value.metric_ref;
  const changeSource = (source: SavedAnalysisSource) => {
    edit.setTouched(true);
    onChange(switchSavedAnalysisSource(value, source));
  };
  return (
    <div className="saved-analysis-editor section-stack">
      {!readOnly && (
        <div className="saved-analysis-source inline-actions" role="group" aria-label="数据源">
          <Button
            type="button"
            aria-pressed={!metricMode}
            disabled={disabled}
            onClick={() => changeSource('inline')}
          >
            内联查询
          </Button>
          <Button
            type="button"
            aria-pressed={metricMode}
            disabled={disabled}
            onClick={() => changeSource('metric')}
          >
            指标引用
          </Button>
        </div>
      )}
      <MetadataFields
        id={id}
        readOnly={readOnly}
        disabled={disabled}
        metricMode={metricMode}
        title={value.title}
        description={value.description ?? ''}
        kind={value.query.kind}
        visualization={value.visualization.kind}
        visualizationError={
          edit.touched
            ? edit.issues.find((issue) => issue.path === '/visualization/kind')?.message
            : undefined
        }
        onTitleChange={(title) => {
          edit.setTouched(true);
          onChange({ ...value, title });
        }}
        onDescriptionChange={(description) => onChange({ ...value, description })}
        onKindChange={edit.setPendingKind}
        onVisualizationChange={(kind) => {
          edit.setTouched(true);
          onChange({ ...value, visualization: { kind } });
        }}
      />
      <p className="saved-analysis-editor-help">{savedDefinitionSummary(value)}</p>
      {metricMode && metricRef ? (
        <MetricReferenceSection
          projectID={projectID}
          environment={environment}
          actorID={actorID}
          value={metricRef}
          readOnly={readOnly}
          disabled={disabled}
          onChange={(ref) => {
            edit.setTouched(true);
            onChange(patchMetricReference(value, ref));
          }}
        />
      ) : (
        !metricMode && (
          <ConfigurationSummary query={value.query} disabled={disabled} onOpen={edit.open} />
        )
      )}
      {edit.touched && edit.issues.length > 0 && !readOnly && (
        <ul className="saved-analysis-editor-errors" aria-label="待完善配置">
          {edit.issues.slice(0, 4).map((issue) => (
            <li key={`${issue.path}:${issue.message}`}>
              {issue.path}，{issue.message}
            </li>
          ))}
        </ul>
      )}
      <SectionDialog
        id={id}
        section={edit.section}
        error={edit.localError}
        disabled={disabled}
        readOnly={readOnly}
        onClose={edit.close}
        onApply={() => {
          if (!edit.apply(catalog.fields)) focus.focusError();
        }}
        onCaptureRemoval={focus.captureRemoval}
        formRef={focus.formRef}
        jsonRef={focus.jsonRef}
        notice={
          edit.section && edit.section !== 'json' && !readOnly ? (
            <CatalogNotice {...catalog.notice} />
          ) : null
        }
      >
        {edit.section === 'events' && (
          <EventsSection
            id={id}
            readOnly={readOnly}
            disabled={disabled}
            pins={query.event_versions ?? []}
            events={catalog.events}
            onChange={(event_versions) => edit.changeQuery({ event_versions })}
          />
        )}
        {edit.section === 'metrics' && (
          <MetricsSection
            id={id}
            readOnly={readOnly}
            disabled={disabled}
            metrics={query.metrics}
            subject={query.subject}
            fields={catalog.fields}
            onMetricsChange={(metrics) => edit.changeQuery({ metrics })}
            onSubjectChange={(subject) => edit.changeQuery({ subject })}
          />
        )}
        {edit.section === 'grouping' && (
          <GroupingSection
            id={id}
            readOnly={readOnly}
            disabled={disabled}
            groups={query.group_by ?? []}
            interval={query.interval}
            limit={edit.numbers.limit ?? ''}
            fields={catalog.fields}
            onGroupsChange={(group_by) => edit.changeQuery({ group_by })}
            onIntervalChange={(interval) => edit.changeQuery({ interval })}
            onLimitChange={(next) => edit.changeNumber('limit', next)}
          />
        )}
        {edit.section === 'filters' && (
          <FiltersSection
            id={id}
            readOnly={readOnly}
            disabled={disabled}
            filter={query.filter}
            fields={catalog.fields}
            filterTexts={edit.filterTexts}
            onFilterChange={(filter) => edit.changeQuery({ filter })}
            onTextsChange={edit.changeFilterTexts}
            onAdvanced={() => edit.open('json')}
          />
        )}
        {edit.section === 'funnel' && (
          <FunnelSection
            id={id}
            readOnly={readOnly}
            disabled={disabled}
            funnel={query.funnel}
            subject={query.subject}
            window={edit.numbers.window ?? ''}
            events={catalog.events}
            onFunnelChange={(funnel) => edit.changeQuery({ funnel })}
            onSubjectChange={(subject) => edit.changeQuery({ subject })}
            onWindowChange={(next) => edit.changeNumber('window', next)}
          />
        )}
        {edit.section === 'retention' && (
          <RetentionSection
            id={id}
            readOnly={readOnly}
            disabled={disabled}
            retention={query.retention}
            subject={query.subject}
            periods={edit.numbers.periods ?? ''}
            events={catalog.events}
            onRetentionChange={(retention) => edit.changeQuery({ retention })}
            onSubjectChange={(subject) => edit.changeQuery({ subject })}
            onPeriodsChange={(next) => edit.changeNumber('periods', next)}
          />
        )}
        {edit.section === 'json' && (
          <AdvancedSection
            id={id}
            readOnly={readOnly}
            disabled={disabled}
            text={edit.text}
            error={edit.localError}
            inputRef={focus.jsonRef}
            onChange={edit.changeText}
          />
        )}
      </SectionDialog>
      <EditorConfirmations
        pendingKind={edit.pendingKind}
        disabled={disabled}
        keepButton={focus.keepButton}
        onCancelKind={() => edit.setPendingKind(undefined)}
        onConfirmKind={edit.confirmKind}
        discarding={edit.discarding}
        onCancelDiscard={() => edit.setDiscarding(false)}
        onConfirmDiscard={edit.discard}
      />
    </div>
  );
}
