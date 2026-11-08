import { useEffect, useMemo, useState } from 'react';
import type { SavedAnalysisDefinition, SavedAnalysisTemplate } from '@evia/api-client';
import type { PublishedField } from '../analysis-model';
import {
  cloneSavedDefinition,
  flatSavedFilters,
  patchSavedQuery,
  replaceSavedKind,
  savedAnalysisIssues,
  savedFilterText,
} from '../saved-analysis-model';
import { applyEditorSection } from './section-application';
import type { EditorSection, SectionNumbers } from './types';

type SectionEditorProps = {
  value: SavedAnalysisDefinition;
  onChange: (next: SavedAnalysisDefinition) => void;
  readOnly: boolean;
  disabled: boolean;
  onValidityChange?: (valid: boolean) => void;
  onPendingChange?: (pending: boolean) => void;
};

/** Own local section drafts and their atomic apply/discard lifecycle. No persistence calls. */
export function useSectionEditor({
  value,
  onChange,
  readOnly,
  disabled,
  onValidityChange,
  onPendingChange,
}: SectionEditorProps) {
  const [section, setSection] = useState<EditorSection | null>(null);
  const [candidate, setCandidate] = useState(value);
  const [opened, setOpened] = useState('');
  const [text, setText] = useState('');
  const [numbers, setNumbers] = useState<SectionNumbers>({});
  const [filterTexts, setFilterTexts] = useState<string[]>([]);
  const [localError, setLocalError] = useState('');
  const [pendingKind, setPendingKind] = useState<SavedAnalysisTemplate['kind']>();
  const [discarding, setDiscarding] = useState(false);
  const [touched, setTouched] = useState(false);
  const issues = useMemo(() => savedAnalysisIssues(value), [value]);
  const pending =
    !readOnly &&
    !!section &&
    (section === 'json'
      ? text !== opened
      : JSON.stringify({ candidate, numbers, filterTexts }) !== opened);
  useEffect(() => {
    onValidityChange?.(!issues.length && !pending && !localError);
  }, [issues.length, pending, localError, onValidityChange]);
  useEffect(() => {
    onPendingChange?.(pending);
  }, [pending, onPendingChange]);

  function open(next: EditorSection) {
    const nextCandidate = cloneSavedDefinition(value);
    const nextNumbers = {
      limit: String(value.query.limit ?? 200),
      window: String(value.query.funnel?.window_seconds ?? 3600),
    };
    const nextFilters = flatSavedFilters(value.query.filter)?.rows.map(savedFilterText) ?? [];
    const json = JSON.stringify(value, null, 2);
    setCandidate(nextCandidate);
    setNumbers(nextNumbers);
    setFilterTexts(nextFilters);
    setText(json);
    setOpened(
      next === 'json'
        ? json
        : JSON.stringify({
            candidate: nextCandidate,
            numbers: nextNumbers,
            filterTexts: nextFilters,
          }),
    );
    setLocalError('');
    setSection(next);
  }
  function close() {
    if (disabled) return;
    if (pending) setDiscarding(true);
    else {
      setSection(null);
      setLocalError('');
    }
  }
  function changeQuery(patch: Partial<SavedAnalysisTemplate>) {
    setLocalError('');
    setCandidate((current) => patchSavedQuery(current, patch));
  }
  function changeNumber(key: keyof SectionNumbers, next: string) {
    setNumbers((current) => ({ ...current, [key]: next }));
    setLocalError('');
  }
  function changeText(next: string) {
    setText(next);
    setLocalError('');
  }
  function changeFilterTexts(next: string[]) {
    setFilterTexts(next);
    setLocalError('');
  }
  function apply(fields: PublishedField[]): boolean {
    if (readOnly || disabled || !section) return true;
    try {
      const next = applyEditorSection({ section, candidate, text, numbers, filterTexts, fields });
      onChange(next);
      setTouched(true);
      setSection(null);
      setLocalError('');
      return true;
    } catch (error) {
      setLocalError(error instanceof Error ? error.message : '配置无效，请检查后重试。');
      return false;
    }
  }
  function confirmKind() {
    if (pendingKind) {
      onChange(replaceSavedKind(value, pendingKind));
      setTouched(true);
    }
    setPendingKind(undefined);
  }
  function discard() {
    setDiscarding(false);
    setSection(null);
    setLocalError('');
  }
  return {
    section,
    candidate,
    text,
    numbers,
    filterTexts,
    localError,
    issues,
    touched,
    pendingKind,
    discarding,
    open,
    close,
    changeQuery,
    changeNumber,
    changeText,
    changeFilterTexts,
    apply,
    setTouched,
    setPendingKind,
    setDiscarding,
    confirmKind,
    discard,
  };
}
