import type { DashboardDefinition, ParameterDeclaration, ParameterValues } from '@evia/api-client';

export const MAX_PARAMETERS = 12;
export const MAX_PARAMETER_LIST = 20;
export const MAX_PARAMETER_TEXT = 1024;

export const parameterTypeOptions = [
  { value: 'string', label: '文本' },
  { value: 'number', label: '数值' },
  { value: 'boolean', label: '布尔' },
] as const;

const operatorLabels: Record<string, string> = {
  eq: '等于',
  neq: '不等于',
  in: '属于',
  prefix: '开头是',
  contains: '包含',
  gt: '大于',
  gte: '大于等于',
  lt: '小于',
  lte: '小于等于',
};

const operatorsByType: Record<string, string[]> = {
  string: ['eq', 'neq', 'in', 'prefix', 'contains'],
  number: ['eq', 'neq', 'in', 'gt', 'gte', 'lt', 'lte'],
  boolean: ['eq', 'neq'],
};

export function parameterOperatorOptions(type: string) {
  return (operatorsByType[type] ?? []).map((value) => ({
    value,
    label: operatorLabels[value] ?? value,
  }));
}

export function parameterOperatorLabel(operator: string) {
  return operatorLabels[operator] ?? operator;
}

type Scalar = string | number | boolean;

function scalarMatchesType(decl: ParameterDeclaration, value: unknown): boolean {
  if (Array.isArray(value)) {
    if (decl.operator !== 'in' || value.length === 0 || value.length > MAX_PARAMETER_LIST)
      return false;
    return value.every((item) => scalarType(decl.type, item));
  }
  return scalarType(decl.type, value);
}

function scalarType(type: string, value: unknown): boolean {
  if (type === 'string')
    return typeof value === 'string' && [...value].length <= MAX_PARAMETER_TEXT;
  if (type === 'number') return typeof value === 'number' && Number.isFinite(value);
  if (type === 'boolean') return typeof value === 'boolean';
  return false;
}

/** Client mirror of the server-side closed set; the server remains authoritative. */
export function parameterIssues(definition: DashboardDefinition): string[] {
  const issues: string[] = [];
  const params = definition.parameters ?? [];
  if (params.length > MAX_PARAMETERS) issues.push('看板参数最多 12 个。');
  const ids = new Set<string>();
  for (const decl of params) {
    const name = decl.parameter_id || '（空标识）';
    if (!decl.parameter_id.trim() || [...decl.parameter_id].length > 64)
      issues.push(`参数 ${name} 的标识需为 1 至 64 个字符。`);
    if (ids.has(decl.parameter_id)) issues.push(`参数标识 ${decl.parameter_id} 重复。`);
    ids.add(decl.parameter_id);
    if (!decl.label.trim() || [...decl.label].length > 128)
      issues.push(`参数 ${name} 的标签需为 1 至 128 个字符。`);
    if (!operatorsByType[decl.type]) {
      issues.push(`参数 ${name} 的类型不受支持。`);
      continue;
    }
    const allowedOperators = operatorsByType[decl.type]!;
    if (!allowedOperators.includes(decl.operator)) issues.push(`参数 ${name} 的运算与类型不匹配。`);
    if (
      decl.default !== undefined &&
      decl.default !== null &&
      !scalarMatchesType(decl, decl.default)
    )
      issues.push(`参数 ${name} 的默认值与类型或运算不匹配。`);
  }
  for (const widget of definition.widgets) {
    const bindings = widget.parameter_bindings ?? [];
    if (bindings.length > MAX_PARAMETERS) {
      issues.push(`组件 ${widget.title} 的参数绑定最多 12 条。`);
      continue;
    }
    const seenParameters = new Set<string>();
    const seenFields = new Set<string>();
    for (const binding of bindings) {
      if (!ids.has(binding.parameter_id))
        issues.push(`组件 ${widget.title} 绑定了未声明参数 ${binding.parameter_id}。`);
      if (seenParameters.has(binding.parameter_id))
        issues.push(`组件 ${widget.title} 重复绑定参数 ${binding.parameter_id}。`);
      seenParameters.add(binding.parameter_id);
      const field = binding.field;
      const eventKey = field.kind === 'property' ? field.event_key : '';
      const schemaVersion = field.kind === 'property' ? field.schema_version : 0;
      const fieldKey = `${field.kind}:${field.key}:${eventKey}:${schemaVersion}`;
      if (seenFields.has(fieldKey))
        issues.push(`组件 ${widget.title} 的同一字段不能绑定多个参数。`);
      seenFields.add(fieldKey);
      if (field.kind === 'property') {
        if (!field.event_key || !field.schema_version)
          issues.push(`组件 ${widget.title} 的属性字段必须限定事件与已发布版本。`);
      } else if (field.kind !== 'system') {
        issues.push(`组件 ${widget.title} 的参数字段类型不受支持。`);
      }
    }
  }
  return issues;
}

/** Parses one runtime input into the scalar/array shape the contract accepts. */
export function parseParameterValue(decl: ParameterDeclaration, text: string): Scalar | Scalar[] {
  const trimmed = text.trim();
  if (decl.type === 'boolean') {
    if (trimmed === 'true') return true;
    if (trimmed === 'false') return false;
    throw new Error(`参数 ${decl.label} 需填写 true 或 false。`);
  }
  if (decl.operator === 'in') {
    let parsed: unknown;
    try {
      parsed = JSON.parse(trimmed);
    } catch {
      throw new Error(`参数 ${decl.label} 需填写 JSON 数组，例如 ["a","b"]。`);
    }
    if (!Array.isArray(parsed) || parsed.length === 0 || parsed.length > MAX_PARAMETER_LIST)
      throw new Error(`参数 ${decl.label} 的数组需包含 1 至 20 个值。`);
    if (!parsed.every((item) => scalarType(decl.type, item)))
      throw new Error(
        `参数 ${decl.label} 的数组元素必须全部为${decl.type === 'number' ? '数值' : '文本'}。`,
      );
    return parsed as Scalar[];
  }
  if (decl.type === 'number') {
    const value = Number(trimmed);
    if (!Number.isFinite(value)) throw new Error(`参数 ${decl.label} 需为有效数值。`);
    return value;
  }
  if ([...trimmed].length > MAX_PARAMETER_TEXT)
    throw new Error(`参数 ${decl.label} 最多 ${MAX_PARAMETER_TEXT} 个字符。`);
  return trimmed;
}

/** Serializes a stored parameter value back into the run input text box. */
export function parameterValueText(value: unknown): string {
  if (value === undefined || value === null) return '';
  return typeof value === 'string' ? value : JSON.stringify(value);
}

export function buildParameterValues(
  declarations: readonly ParameterDeclaration[],
  texts: Record<string, string>,
): ParameterValues {
  const values: ParameterValues = {};
  for (const decl of declarations) {
    const text = texts[decl.parameter_id] ?? '';
    if (text.trim() === '') continue;
    values[decl.parameter_id] = parseParameterValue(decl, text);
  }
  return values;
}
