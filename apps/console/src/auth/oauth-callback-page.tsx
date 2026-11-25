import { useEffect, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { api, ApiError, type OAuthCallbackResponse } from '@evia/api-client';
import { AuthLayout, Button, ErrorPanel, LoadingPanel } from '@evia/design-system';

const inFlightCallbacks = new Map<string, Promise<OAuthCallbackResponse>>();

function safeNextPath(raw: string | null | undefined): string {
  return raw && raw.startsWith('/') && !raw.startsWith('//') ? raw : '/projects';
}

function exchangeCallbackOnce(code: string, state: string): Promise<OAuthCallbackResponse> {
  const key = `${state}:${code}`;
  let promise = inFlightCallbacks.get(key);
  if (!promise) {
    promise = api.completeOAuthCallback({ code, state });
    inFlightCallbacks.set(key, promise);
  }
  return promise;
}

function describeProviderError(errorCode: string, description: string | null): string {
  if (errorCode === 'access_denied') {
    return '你已取消单点登录授权，如需进入控制台请重新发起登录。';
  }
  const cleanDescription = description?.trim();
  if (cleanDescription) {
    return `单点登录授权未完成：${cleanDescription}`;
  }
  return `单点登录授权失败（${errorCode}），请返回登录页重试。`;
}

export function OAuthCallbackPage() {
  const cache = useQueryClient();
  const navigate = useNavigate();
  const [params] = useSearchParams();

  const providerError = params.get('error');
  const providerErrorDescription = params.get('error_description');
  const code = (params.get('code') ?? '').trim();
  const state = (params.get('state') ?? '').trim();

  const [failure, setFailure] = useState<{ message: string; requestID?: string } | null>(() => {
    if (providerError) {
      return { message: describeProviderError(providerError, providerErrorDescription) };
    }
    if (!code || !state) {
      return { message: '单点登录回调缺少必要参数，请返回登录页重新发起授权。' };
    }
    return null;
  });

  useEffect(() => {
    if (providerError || !code || !state) return;

    let active = true;
    exchangeCallbackOnce(code, state)
      .then((result) => {
        if (!active) return;
        cache.removeQueries({ predicate: (query) => query.queryKey[0] !== 'session' });
        cache.setQueryData(['session'], result.session);
        void navigate(safeNextPath(result.next_path), { replace: true });
      })
      .catch((error: unknown) => {
        if (!active) return;
        if (error instanceof ApiError) {
          setFailure({ message: error.message, requestID: error.requestID });
        } else {
          setFailure({
            message: error instanceof Error ? error.message : '单点登录完成失败，请重试。',
          });
        }
      });

    return () => {
      active = false;
    };
  }, [cache, code, navigate, providerError, state]);

  return (
    <AuthLayout
      onNavigate={(href) => void navigate(href)}
      topAction={
        <Link to="/login" className="auth-back-link">
          ← 返回登录
        </Link>
      }
    >
      <h1>单点登录</h1>
      {failure ? (
        <>
          <p className="auth-subtitle">未能完成本次单点登录授权。</p>
          <ErrorPanel message={failure.message} requestID={failure.requestID} />
          <div className="console-form" style={{ marginTop: 20 }}>
            <Button
              variant="primary"
              className="auth-submit"
              onClick={() => void navigate('/login', { replace: true })}
            >
              返回登录页重新授权
            </Button>
          </div>
        </>
      ) : (
        <>
          <p className="auth-subtitle">正在校验授权凭证并建立本地会话，请稍候。</p>
          <LoadingPanel label="正在完成单点登录" />
        </>
      )}
    </AuthLayout>
  );
}
