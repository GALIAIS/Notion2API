'use client';

import { useEffect, useState } from 'react';
import { LockKeyhole, ShieldAlert, Sparkles } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';

export function LoginOverlay({
  passwordConfigured,
  message,
  busy,
  onSubmit,
}: {
  passwordConfigured: boolean;
  message: string;
  busy: boolean;
  onSubmit: (password: string) => Promise<void>;
}) {
  const [password, setPassword] = useState('');
  const [localMessage, setLocalMessage] = useState('');

  useEffect(() => {
    setLocalMessage(message);
  }, [message]);

  return (
    <div className="fixed inset-0 z-50 overflow-hidden">
      <div className="auth-shell absolute inset-0" />
      {/* Decorative blurred orbs */}
      <div className="orb orb-indigo orb-animate left-[6%] top-[14%] h-72 w-72" />
      <div className="orb orb-violet orb-animate right-[8%] top-[18%] h-80 w-80" style={ { animationDelay: '1.6s' } } />
      <div className="orb orb-indigo orb-animate bottom-[-6%] left-[34%] h-72 w-72 opacity-40" style={ { animationDelay: '3s' } } />

      <div className="relative z-10 flex min-h-screen items-center justify-center px-4 py-10">
        <div className="w-full max-w-[460px] space-y-7">
          <div className="space-y-4 text-center">
            <div className="brand-badge mx-auto size-14 text-xl">N</div>
            <div className="space-y-2">
              <div className="inline-flex items-center gap-1.5 rounded-full border border-[color-mix(in_oklab,var(--primary)_24%,transparent)] bg-[color-mix(in_oklab,var(--primary)_10%,var(--card))] px-3 py-1 text-[11px] font-bold uppercase tracking-[0.16em] text-[color-mix(in_oklab,var(--primary)_70%,var(--foreground))]">
                <Sparkles className="size-3.5" />
                Enterprise Console
              </div>
              <h1 className="text-[34px] font-bold leading-[1.1] tracking-tight md:text-[40px]">
                Nation<span className="text-gradient-brand">2API</span>
              </h1>
              <p className="mx-auto max-w-md text-[15px] leading-7 text-muted-foreground">
                统一管理账号、模型、会话与协议配置。安全、可靠、随时响应。
              </p>
            </div>
          </div>

          <Card className="panel-card elevated w-full overflow-hidden">
            <CardHeader className="px-6 pt-6">
              <div className="mb-3 flex size-11 items-center justify-center rounded-xl bg-[color-mix(in_oklab,var(--primary)_12%,var(--card))] text-primary shadow-[0_6px_18px_-8px_rgb(var(--primary-rgb)/0.5)]">
                {passwordConfigured ? <LockKeyhole className="size-[20px]" /> : <ShieldAlert className="size-[20px]" />}
              </div>
              <CardTitle className="text-[19px] font-semibold tracking-tight">
                {passwordConfigured ? '进入控制台' : '管理面尚未就绪'}
              </CardTitle>
              <CardDescription className="mt-1 leading-6">
                {passwordConfigured
                  ? '先完成管理密码认证。登录后会恢复账号、模型、会话和配置快照。'
                  : '请先在配置中设置 `admin.password`，否则 WebUI 不开放敏感操作。'}
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4 px-6 pb-6">
              {passwordConfigured ? (
                <form
                  className="space-y-3"
                  onSubmit={async (event) => {
                    event.preventDefault();
                    setLocalMessage('登录中…');
                    try {
                      await onSubmit(password);
                      setPassword('');
                    } catch (error) {
                      setLocalMessage(error instanceof Error ? error.message : '登录失败');
                    }
                  }}
                >
                  <Input
                    type="password"
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    placeholder="请输入管理密码"
                    className="h-11 text-[14px]"
                    autoFocus
                  />
                  <Button type="submit" disabled={busy || !password.trim()} className="h-11 w-full justify-center">
                    {busy ? '登录中…' : '登录控制台'}
                  </Button>
                </form>
              ) : null}
              <div className="surface-subtle px-3.5 py-2.5 text-[12.5px] leading-6 text-muted-foreground">
                {localMessage || (passwordConfigured ? '请输入密码登录。' : '未检测到 admin.password。')}
              </div>
            </CardContent>
          </Card>

          <p className="text-center text-[12px] text-muted-foreground">
            © Nation2API · Crafted for serious operators
          </p>
        </div>
      </div>
    </div>
  );
}
