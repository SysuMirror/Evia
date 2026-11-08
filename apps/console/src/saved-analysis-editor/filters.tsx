import { Button, CodeBlock, Field, Input, Select } from '@evia/design-system';
import {
  fieldOperators,
  operators,
  type PublishedField,
  type QueryFilter,
} from '../analysis-model';
import {
  flatSavedFilters,
  savedFieldID,
  savedFilterText,
  type SavedLeafFilter,
} from '../saved-analysis-model';
import { FieldSelect } from './selection-controls';
import type { SectionControlProps } from './types';

type FiltersSectionProps = SectionControlProps & {
  filter: QueryFilter | undefined;
  fields: PublishedField[];
  filterTexts: string[];
  onFilterChange: (next: QueryFilter | undefined) => void;
  onTextsChange: (next: string[]) => void;
  onAdvanced: () => void;
};
export function FiltersSection({
  id,
  filter,
  fields,
  filterTexts,
  onFilterChange,
  onTextsChange,
  onAdvanced,
  readOnly,
  disabled,
}: FiltersSectionProps) {
  const leafRows = flatSavedFilters(filter);
  const blocked = readOnly || disabled;
  function changeRows(rows: SavedLeafFilter[], join = leafRows?.join ?? 'and') {
    onFilterChange(
      !rows.length
        ? undefined
        : rows.length === 1 && join === 'and' && filter && !('args' in filter)
          ? rows[0]
          : { op: join, args: rows },
    );
  }
  if (!leafRows)
    return (
      <>
        <p>当前包含嵌套条件，请使用高级 JSON 编辑，其他配置操作会保留这些条件。</p>
        <CodeBlock label="完整筛选条件">{JSON.stringify(filter, null, 2)}</CodeBlock>
        <Button disabled={disabled} onClick={onAdvanced}>
          查看高级 JSON
        </Button>
      </>
    );
  return (
    <>
      <Field label="条件关系" htmlFor={`${id}-join`}>
        <Select
          id={`${id}-join`}
          disabled={blocked}
          value={leafRows.join}
          options={[
            { value: 'and', label: '同时满足' },
            { value: 'or', label: '满足任意一项' },
          ]}
          onChange={(event) => changeRows(leafRows.rows, event.target.value as 'and' | 'or')}
        />
      </Field>
      {leafRows.rows.map((item, index) => {
        const found = fields.find((field) => field.id === savedFieldID(item.field));
        const choices = found ? fieldOperators(item.field.key, found.type) : Object.keys(operators);
        const update = (next: SavedLeafFilter) =>
          changeRows(leafRows.rows.map((row, rowIndex) => (rowIndex === index ? next : row)));
        return (
          <div className="saved-analysis-editor-row" key={index}>
            <FieldSelect
              id={id}
              field={item.field}
              fields={fields}
              disabled={blocked}
              label={`筛选 ${index + 1} 字段`}
              onChange={(field) => {
                if (field) {
                  update({ op: 'eq', field, value: '' });
                  onTextsChange(
                    filterTexts.map((text, textIndex) => (textIndex === index ? '' : text)),
                  );
                }
              }}
            />
            <Field label={`筛选 ${index + 1} 运算`} htmlFor={`${id}-filter-op-${index}`}>
              <Select
                id={`${id}-filter-op-${index}`}
                disabled={blocked}
                value={item.op}
                options={[...new Set([item.op, ...choices])].map((op) => ({
                  value: op,
                  label: operators[op] ?? op,
                }))}
                onChange={(event) => {
                  const op = event.target.value as SavedLeafFilter['op'];
                  update(
                    op === 'is_present' || op === 'is_missing'
                      ? { op, field: item.field }
                      : { op, field: item.field, value: 'value' in item ? item.value : '' },
                  );
                }}
              />
            </Field>
            {'value' in item && (
              <Field
                label={`筛选 ${index + 1} 值`}
                htmlFor={`${id}-filter-value-${index}`}
                hint={
                  found?.type.startsWith('array:') || item.op === 'in'
                    ? 'JSON 数组，例如 [0, 1] 或 [false, true]'
                    : found?.type === 'boolean'
                      ? 'true 或 false'
                      : undefined
                }
              >
                <Input
                  id={`${id}-filter-value-${index}`}
                  readOnly={readOnly}
                  disabled={disabled}
                  value={filterTexts[index] ?? savedFilterText(item)}
                  onChange={(event) => {
                    const next = [...filterTexts];
                    next[index] = event.target.value;
                    onTextsChange(next);
                  }}
                />
              </Field>
            )}
            {!readOnly && (
              <Button
                disabled={disabled}
                onClick={() => {
                  changeRows(leafRows.rows.filter((_, rowIndex) => rowIndex !== index));
                  onTextsChange(filterTexts.filter((_, textIndex) => textIndex !== index));
                }}
              >
                移除筛选 {index + 1}
              </Button>
            )}
          </div>
        );
      })}
      {!readOnly && (
        <Button
          disabled={disabled || leafRows.rows.length >= 20}
          onClick={() => {
            changeRows([
              ...leafRows.rows,
              { op: 'eq', field: { kind: 'system', key: 'event_key' }, value: '' },
            ]);
            onTextsChange([...filterTexts, '']);
          }}
        >
          添加筛选
        </Button>
      )}
    </>
  );
}
