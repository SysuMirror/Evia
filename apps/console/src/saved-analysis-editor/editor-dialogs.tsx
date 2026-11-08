import type { MouseEventHandler, ReactNode, RefObject } from 'react';
import type { SavedAnalysisTemplate } from '@evia/api-client';
import { Button, Modal } from '@evia/design-system';
import { sectionTitles, type EditorSection } from './types';

type SectionDialogProps = {
  id: string;
  section: EditorSection | null;
  error: string;
  disabled: boolean;
  readOnly: boolean;
  onClose: () => void;
  onApply: () => void;
  onCaptureRemoval: MouseEventHandler<HTMLFormElement>;
  formRef: RefObject<HTMLFormElement | null>;
  jsonRef: RefObject<HTMLTextAreaElement | null>;
  notice: ReactNode;
  children: ReactNode;
};
export function SectionDialog({
  id,
  section,
  error,
  disabled,
  readOnly,
  onClose,
  onApply,
  onCaptureRemoval,
  formRef,
  jsonRef,
  notice,
  children,
}: SectionDialogProps) {
  return (
    <Modal
      open={!!section}
      title={section ? sectionTitles[section] : ''}
      onRequestClose={onClose}
      busy={disabled}
      initialFocusRef={section === 'json' ? jsonRef : undefined}
      footer={
        <>
          <Button disabled={disabled} onClick={onClose}>
            关闭
          </Button>
          {!readOnly && (
            <Button variant="primary" type="submit" form={`${id}-section`} disabled={disabled}>
              应用配置
            </Button>
          )}
        </>
      }
    >
      <form
        id={`${id}-section`}
        ref={formRef}
        aria-describedby={error ? `${id}-section-error` : undefined}
        className="section-stack"
        onClickCapture={onCaptureRemoval}
        onSubmit={(event) => {
          event.preventDefault();
          event.stopPropagation();
          onApply();
        }}
      >
        {error && (
          <p id={`${id}-section-error`} role="alert" className="saved-analysis-editor-errors">
            {error}
          </p>
        )}
        {notice}
        {children}
      </form>
    </Modal>
  );
}

type EditorConfirmationsProps = {
  pendingKind: SavedAnalysisTemplate['kind'] | undefined;
  disabled: boolean;
  keepButton: RefObject<HTMLButtonElement | null>;
  onCancelKind: () => void;
  onConfirmKind: () => void;
  discarding: boolean;
  onCancelDiscard: () => void;
  onConfirmDiscard: () => void;
};
export function EditorConfirmations({
  pendingKind,
  disabled,
  keepButton,
  onCancelKind,
  onConfirmKind,
  discarding,
  onCancelDiscard,
  onConfirmDiscard,
}: EditorConfirmationsProps) {
  return (
    <>
      <Modal
        open={!!pendingKind}
        title="替换分析类型？"
        onRequestClose={onCancelKind}
        initialFocusRef={keepButton}
        footer={
          <>
            <Button ref={keepButton} onClick={onCancelKind}>
              保留当前配置
            </Button>
            <Button variant="danger" disabled={disabled} onClick={onConfirmKind}>
              替换配置
            </Button>
          </>
        }
      >
        <p>替换会清除当前的事件版本、指标、分组、筛选和漏斗条件，标题与描述会保留。</p>
      </Modal>
      <Modal
        open={discarding}
        title="放弃本节修改？"
        onRequestClose={onCancelDiscard}
        initialFocusRef={keepButton}
        footer={
          <>
            <Button ref={keepButton} onClick={onCancelDiscard}>
              继续编辑
            </Button>
            <Button variant="danger" onClick={onConfirmDiscard}>
              放弃本节修改
            </Button>
          </>
        }
      >
        <p>未应用的配置修改将丢失。</p>
      </Modal>
    </>
  );
}
