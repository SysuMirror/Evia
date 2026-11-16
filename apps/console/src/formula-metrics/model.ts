import type { FormulaExpression, FormulaMetricDefinition, MetricReference } from '@evia/api-client';

export type FormulaOperator = 'add' | 'subtract' | 'multiply' | 'divide' | 'convert';
export type ConvertTargetUnit = 'millisecond' | 'second';
export type OperandKind = 'metric' | 'constant';
export type FormulaMetricUnit =
  | 'event'
  | 'raw_uid'
  | 'anonymous_id'
  | 'distinct_value'
  | 'number'
  | 'millisecond'
  | 'second'
  | 'byte';

export const formulaOperators: FormulaOperator[] = [
  'add',
  'subtract',
  'multiply',
  'divide',
  'convert',
];

export const operatorLabels: Record<FormulaOperator, string> = {
  add: '加法 add（+）',
  subtract: '减法 subtract（−）',
  multiply: '乘法 multiply（×）',
  divide: '除法 divide（÷）',
  convert: '单位换算 convert（毫秒↔秒）',
};

export const convertTargetUnits: ConvertTargetUnit[] = ['millisecond', 'second'];

export const convertTargetLabels: Record<ConvertTargetUnit, string> = {
  millisecond: '毫秒 millisecond（源须为秒）',
  second: '秒 second（源须为毫秒）',
};

export const formulaUnits: FormulaMetricUnit[] = [
  'event',
  'raw_uid',
  'anonymous_id',
  'distinct_value',
  'number',
  'millisecond',
  'second',
  'byte',
];

export const formulaUnitLabels: Record<FormulaMetricUnit, string> = {
  event: '事件次数 event',
  raw_uid: '原始 UID raw_uid',
  anonymous_id: '匿名标识 anonymous_id',
  distinct_value: '不同值 distinct_value',
  number: '数值 number',
  millisecond: '毫秒 millisecond',
  second: '秒 second',
  byte: '字节 byte',
};

export interface OperandState {
  kind: OperandKind;
  // Used when kind === 'metric'.
  metricID: string;
  version: string;
  // Used when kind === 'constant'.
  constant: string;
}

export interface FormulaFormState {
  title: string;
  description: string;
  unit: FormulaMetricUnit;
  operator: FormulaOperator;
  left: OperandState;
  right: OperandState;
  // Used only when operator === 'convert'.
  targetUnit: ConvertTargetUnit;
}

export function defaultOperand(kind: OperandKind = 'metric'): OperandState {
  return { kind, metricID: '', version: '1', constant: '' };
}

export function defaultFormState(): FormulaFormState {
  return {
    title: '',
    description: '',
    unit: 'number',
    operator: 'divide',
    left: defaultOperand('metric'),
    right: defaultOperand('metric'),
    targetUnit: 'second',
  };
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const MAX_SAFE = 9007199254740991;

function buildOperand(
  state: OperandState,
  errors: string[],
  label: string,
): FormulaExpression | undefined {
  if (state.kind === 'constant') {
    const value = Number(state.constant);
    if (state.constant.trim() === '' || !Number.isFinite(value) || Math.abs(value) > MAX_SAFE) {
      errors.push(`${label}常量必须是安全范围内的有限数值。`);
      return undefined;
    }
    return { op: 'constant', value };
  }
  const version = Number(state.version.trim());
  if (!UUID.test(state.metricID.trim()) || !Number.isInteger(version) || version < 1) {
    errors.push(`${label}需要选择已发布的原子指标版本（合法 UUID 与不小于 1 的版本）。`);
    return undefined;
  }
  const ref: MetricReference = { metric_id: state.metricID.trim(), version };
  return { op: 'metric', metric: ref };
}

/** Convert the builder form into a wire composite definition. */
export function toFormulaDefinition(state: FormulaFormState): {
  definition?: FormulaMetricDefinition;
  errors: string[];
} {
  const errors: string[] = [];
  const title = state.title.trim();
  if (title.length < 1 || title.length > 128) errors.push('指标标题需为 1–128 个非空白字符。');
  if (state.description.length > 2000) errors.push('描述不能超过 2000 个字符。');

  const left = buildOperand(state.left, errors, '左操作数');
  if (state.operator === 'convert') {
    if (errors.length || !left) return { errors };
    const expression: FormulaExpression = {
      op: 'convert',
      left,
      target_unit: state.targetUnit,
    };
    return {
      definition: {
        title,
        description: state.description.trim(),
        formula: { expression, unit: state.unit },
      },
      errors: [],
    };
  }

  const right = buildOperand(state.right, errors, '右操作数');
  if (left && right && left.op === 'metric' && right.op === 'metric') {
    const same =
      left.metric!.metric_id === right.metric!.metric_id &&
      left.metric!.version === right.metric!.version;
    if (same) errors.push('两个操作数不能引用完全相同的指标版本，请选择不同引用或改用常量。');
  }
  if (errors.length || !left || !right) return { errors };

  const expression: FormulaExpression = { op: state.operator, left, right };
  const definition: FormulaMetricDefinition = {
    title,
    description: state.description.trim(),
    formula: { expression, unit: state.unit },
  };
  return { definition, errors: [] };
}

function readOperand(node: FormulaExpression | undefined): OperandState {
  if (!node) return defaultOperand();
  if (node.op === 'constant')
    return { kind: 'constant', metricID: '', version: '1', constant: String(node.value ?? '') };
  if (node.op === 'metric' && node.metric)
    return {
      kind: 'metric',
      metricID: node.metric.metric_id,
      version: String(node.metric.version),
      constant: '',
    };
  return defaultOperand();
}

/** Populate the builder from a stored binary composite definition. */
export function fromFormulaDefinition(definition: FormulaMetricDefinition): FormulaFormState {
  const state = defaultFormState();
  state.title = definition.title;
  state.description = definition.description ?? '';
  state.unit = definition.formula.unit;
  const expression = definition.formula.expression;
  if (expression.op === 'convert') {
    state.operator = 'convert';
    state.left = readOperand(expression.left);
    state.targetUnit = expression.target_unit === 'millisecond' ? 'millisecond' : 'second';
  } else if (expression.op !== 'constant' && expression.op !== 'metric') {
    state.operator = expression.op;
    state.left = readOperand(expression.left);
    state.right = readOperand(expression.right);
  }
  return state;
}

export const MAX_FORMULA_RANGE_DAYS = 31;

export interface FormulaRunForm {
  from: string;
  to: string;
  timezone: string;
  interval: '' | 'minute' | 'hour' | 'day';
  limit: string;
}

export function defaultRunForm(timezone: string, now: Date = new Date()): FormulaRunForm {
  const to = now.getTime();
  const from = to - 7 * 86400_000;
  return {
    from: new Date(from).toISOString().slice(0, 16),
    to: new Date(to).toISOString().slice(0, 16),
    timezone,
    interval: '',
    limit: '200',
  };
}

export function buildFormulaRun(form: FormulaRunForm): {
  execute?: {
    time_range: { from: string; to: string };
    timezone: string;
    interval?: string;
    limit: number;
  };
  error?: string;
} {
  const from = new Date(form.from);
  const to = new Date(form.to);
  if (Number.isNaN(from.getTime()) || Number.isNaN(to.getTime()))
    return { error: '请提供有效的运行起止时间。' };
  if (!(to.getTime() > from.getTime())) return { error: '结束时间必须晚于开始时间。' };
  if (to.getTime() - from.getTime() > MAX_FORMULA_RANGE_DAYS * 86400_000)
    return { error: `运行范围最长 ${MAX_FORMULA_RANGE_DAYS} 天。` };
  const limit = Number(form.limit);
  if (!Number.isInteger(limit) || limit < 1 || limit > 500)
    return { error: '结果行数上限需为 1–500 的整数。' };
  const execute = {
    time_range: { from: from.toISOString(), to: to.toISOString() },
    timezone: form.timezone.trim() || 'UTC',
    limit,
    ...(form.interval ? { interval: form.interval } : {}),
  };
  return { execute };
}

export function formatFormulaInstant(instant: string): string {
  const parsed = new Date(instant);
  if (Number.isNaN(parsed.getTime())) return instant;
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())} ${pad(
    parsed.getHours(),
  )}:${pad(parsed.getMinutes())}`;
}
