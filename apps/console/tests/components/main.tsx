import { StrictMode, useState } from 'react';
import { createRoot } from 'react-dom/client';
import {
  Button,
  Checkbox,
  CopyButton,
  DesignProvider,
  Field,
  Input,
  Select,
  Textarea,
  useToast,
} from '@evia/design-system';

// Isolated development fixture. This HTML entry is not a product route or a build input.
function ComponentFixture() {
  const toast = useToast();
  const [environment, setEnvironment] = useState('development');
  const [notes, setNotes] = useState('初始说明');
  const [enabled, setEnabled] = useState(false);
  const [lastFocus, setLastFocus] = useState('无');
  const [submissions, setSubmissions] = useState(0);
  const [clicks, setClicks] = useState(0);
  const [disabledClicks, setDisabledClicks] = useState(0);
  const [pressed, setPressed] = useState(false);
  const [copyValue, setCopyValue] = useState('值 A');
  const [copyMounted, setCopyMounted] = useState(true);

  return (
    <main style={{ maxWidth: 800, margin: '24px auto', padding: 24 }}>
      <h1>组件交互测试</h1>
      <section aria-labelledby="native-title">
        <h2 id="native-title">原生控件</h2>
        <form
          aria-label="控件表单"
          onSubmit={(event) => {
            event.preventDefault();
            setSubmissions((value) => value + 1);
          }}
        >
          <Field label="环境" htmlFor="fixture-environment" hint="使用方向键选择环境。">
            <Select
              id="fixture-environment"
              value={environment}
              onFocus={() => setLastFocus('环境')}
              onChange={(event) => setEnvironment(event.target.value)}
              options={[
                { value: 'development', label: '开发' },
                { value: 'testing', label: '测试' },
                { value: 'retired', label: '已停用', disabled: true },
                { value: 'production', label: '生产' },
              ]}
            />
          </Field>
          <Field label="说明" htmlFor="fixture-notes">
            <Textarea
              id="fixture-notes"
              value={notes}
              onChange={(event) => setNotes(event.target.value)}
            />
          </Field>
          <Checkbox checked={enabled} onChange={(event) => setEnabled(event.target.checked)}>
            启用记录
          </Checkbox>
          <Field label="只读编号" htmlFor="fixture-readonly-input">
            <Input id="fixture-readonly-input" readOnly value="只读编号 123" />
          </Field>
          <Field label="只读说明" htmlFor="fixture-readonly-textarea">
            <Textarea id="fixture-readonly-textarea" readOnly value={'第一行\n第二行'} />
          </Field>
          <Field label="禁用输入" htmlFor="fixture-disabled-input">
            <Input id="fixture-disabled-input" disabled value="不能修改" />
          </Field>
          <Field label="禁用多行输入" htmlFor="fixture-disabled-textarea">
            <Textarea id="fixture-disabled-textarea" disabled value="不能修改多行内容" />
          </Field>
          <Field label="禁用且只读输入" htmlFor="fixture-disabled-readonly-input">
            <Input id="fixture-disabled-readonly-input" disabled readOnly value="禁用且只读" />
          </Field>
          <Field label="禁用且只读多行输入" htmlFor="fixture-disabled-readonly-textarea">
            <Textarea
              id="fixture-disabled-readonly-textarea"
              disabled
              readOnly
              value="禁用且只读多行内容"
            />
          </Field>
          <Checkbox disabled onChange={() => setDisabledClicks((value) => value + 1)}>
            禁用选项
          </Checkbox>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginBlock: 16 }}>
            <Button
              type="submit"
              onClick={(event) => {
                event.preventDefault();
                setClicks((value) => value + 1);
              }}
            >
              阻止提交
            </Button>
            <Button type="submit">提交表单</Button>
            <Button disabled onClick={() => setDisabledClicks((value) => value + 1)}>
              禁用操作
            </Button>
          </div>
        </form>
        <output aria-label="选择结果">{environment}</output>{' '}
        <output aria-label="说明结果">{notes}</output>{' '}
        <output aria-label="勾选结果">{enabled ? '已启用' : '未启用'}</output>{' '}
        <output aria-label="焦点事件">{lastFocus}</output>{' '}
        <output aria-label="提交次数">{submissions}</output>{' '}
        <output aria-label="点击次数">{clicks}</output>{' '}
        <output aria-label="禁用激活次数">{disabledClicks}</output>
      </section>
      <section aria-labelledby="button-title">
        <h2 id="button-title">按钮状态</h2>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
          <Button variant="primary">主要按钮</Button>
          <Button variant="secondary">次要按钮</Button>
          <Button variant="danger">危险按钮</Button>
          <Button variant="ghost">幽灵按钮</Button>
          <Button variant="link">链接按钮</Button>
          <Button aria-pressed={pressed} onClick={() => setPressed((value) => !value)}>
            切换选择
          </Button>
        </div>
      </section>
      <section aria-labelledby="copy-title">
        <h2 id="copy-title">异步复制</h2>
        <Field label="待复制值" htmlFor="fixture-copy-value">
          <Input
            id="fixture-copy-value"
            value={copyValue}
            onChange={(event) => setCopyValue(event.target.value)}
          />
        </Field>
        <Button onClick={() => setCopyMounted((value) => !value)}>
          {copyMounted ? '卸载复制控件' : '挂载复制控件'}
        </Button>
        {copyMounted && <CopyButton value={copyValue} label="复制测试值" />}
      </section>
      <section aria-labelledby="toast-title">
        <h2 id="toast-title">通知生命周期</h2>
        <Button onClick={() => toast.success('测试成功通知')}>显示成功通知</Button>{' '}
        <Button onClick={() => toast.error('测试错误通知')}>显示错误通知</Button>
      </section>
    </main>
  );
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <DesignProvider>
      <ComponentFixture />
    </DesignProvider>
  </StrictMode>,
);
