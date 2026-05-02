'use client';

import { useMemo, useState } from 'react';
import { useTheme } from 'next-themes';
import {
  AlertTriangle,
  LoaderCircle,
  Maximize2,
  Menu,
  Minimize2,
  Monitor,
  Moon,
  RefreshCcw,
  Sun,
} from 'lucide-react';
import { AccountsPanel } from '@/components/admin/accounts-panel';
import { AdminSidebar } from '@/components/admin/admin-sidebar';
import { ConversationsPanel } from '@/components/admin/conversations-panel';
import { DashboardPanel } from '@/components/admin/dashboard-panel';
import { ACCENT_THEME_OPTIONS, useAccentTheme } from '@/components/layout/theme-provider';
import { LoginOverlay } from '@/components/admin/login-overlay';
import { ModelsPanel } from '@/components/admin/models-panel';
import { SettingsPanel } from '@/components/admin/settings-panel';
import { TesterPanel } from '@/components/admin/tester-panel';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { useAdminConsole } from '@/hooks/use-admin-console';
import type { TabKey } from '@/lib/services/admin/types';

const TAB_LABEL: Record<TabKey, string> = {
  dashboard: '状态',
  tester: 'API Tester',
  conversations: '会话',
  settings: '设置',
  accounts: '账号',
  models: '模型',
};

export function AdminConsole() {
  const consoleState = useAdminConsole();
  const [activeTab, setActiveTab] = useState<TabKey>('dashboard');
  const [loginBusy, setLoginBusy] = useState(false);
  const [loginMessage, setLoginMessage] = useState('');
  const [isFullWidth, setIsFullWidth] = useState(false);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const { theme, setTheme } = useTheme();
  const { accentTheme, setAccentTheme } = useAccentTheme();

  const {
    verify,
    configPayload,
    versionPayload,
    healthPayload,
    accountsPayload,
    conversations,
    selectedConversation,
    selectedConversationId,
    streamState,
    bootLoading,
    bootError,
    setSelectedConversationId,
    loadConversationDetail,
    loadConversations,
    refreshConversations,
    refreshAll,
    refreshAccounts,
    refreshConfigBundle,
    login,
    logout,
    deleteConversation,
    batchDeleteConversations,
    services,
  } = consoleState;

  const authenticated = Boolean(verify?.authenticated);
  const passwordConfigured = Boolean(verify?.password_configured);
  const shouldShowOverlay = Boolean(verify) && !authenticated;
  const models = configPayload?.models || [];
  const defaultModel =
    configPayload?.config?.default_model ||
    configPayload?.config?.model_id ||
    versionPayload?.default_model;
  const defaultWebSearch = Boolean(configPayload?.config?.features?.use_web_search);

  const authState = useMemo(() => {
    if (authenticated) return '已认证';
    if (!verify?.admin_enabled) return '未启用';
    if (!passwordConfigured) return '未配置密码';
    return '待登录';
  }, [authenticated, passwordConfigured, verify?.admin_enabled]);

  const themeMode = theme === 'light' || theme === 'dark' || theme === 'system' ? theme : 'system';

  const renderActivePanel = () => {
    switch (activeTab) {
      case 'tester':
        return (
          <TesterPanel
            models={models}
            defaultModel={defaultModel}
            defaultWebSearch={defaultWebSearch}
            onRun={async (payload) => {
              const result = await services.testPrompt(payload);
              await loadConversations();
              return result;
            }}
          />
        );
      case 'conversations':
        return (
          <ConversationsPanel
            conversations={conversations}
            selectedConversationId={selectedConversationId}
            selectedConversation={selectedConversation}
            streamState={streamState}
            onRefresh={refreshConversations}
            onSelect={async (conversationId) => {
              setSelectedConversationId(conversationId);
              await loadConversationDetail(conversationId, false);
            }}
            onDelete={deleteConversation}
            onBatchDelete={batchDeleteConversations}
          />
        );
      case 'settings':
        return configPayload ? (
          <SettingsPanel
            config={configPayload.config}
            models={models}
            adminPasswordSet={Boolean(configPayload.secrets?.admin_password_set)}
            onSave={async (config) => {
              const payload = await services.updateSettings(config);
              await refreshAll();
              return payload;
            }}
            onImport={async (config) => {
              const payload = await services.importConfig(config);
              await refreshAll();
              return payload;
            }}
            onExport={services.exportConfig}
            onCreateSnapshot={services.createConfigSnapshot}
            onListSnapshot={services.listConfigSnapshots}
            onTestPrompt={async (payload) => {
              const result = await services.testPrompt(payload);
              await loadConversations();
              return result;
            }}
          />
        ) : null;
      case 'accounts':
        return (
          <AccountsPanel
            accountsPayload={accountsPayload}
            models={models}
            defaultModel={defaultModel}
            onRefresh={refreshAccounts}
            onStartLogin={async (email) => {
              const payload = await services.startAccountLogin(email);
              await refreshAccounts();
              await refreshConfigBundle();
              return payload;
            }}
            onVerifyCode={async (email, code) => {
              const payload = await services.verifyAccountCode(email, code);
              await refreshAll();
              return payload;
            }}
            onImportAccount={async (payload) => {
              const result = await services.importAccount(payload);
              await refreshAll();
              return result;
            }}
            onQuickTest={async (payload) => {
              const result = await services.quickTestAccount(payload);
              await loadConversations();
              return result;
            }}
            onActivate={async (email) => {
              const payload = await services.activateAccount(email);
              await refreshAll();
              return payload;
            }}
            onDelete={async (email) => {
              const payload = await services.deleteAccount(email);
              await refreshAll();
              return payload;
            }}
            onSaveAccountSettings={async (payload) => {
              const result = await services.saveAccountSettings(payload);
              await refreshAccounts();
              await refreshConfigBundle();
              return result;
            }}
          />
        );
      case 'models':
        return <ModelsPanel models={models} defaultModel={defaultModel} />;
      case 'dashboard':
      default:
        return (
          <DashboardPanel
            configPayload={configPayload}
            versionPayload={versionPayload}
            healthPayload={healthPayload}
            accountsPayload={accountsPayload}
          />
        );
    }
  };

  if (bootLoading && !verify) {
    return (
      <main className="console-surface flex min-h-screen items-center justify-center px-4 py-8">
        <div className="orb orb-indigo orb-animate left-[10%] top-[16%] h-72 w-72" />
        <div className="orb orb-violet orb-animate right-[10%] bottom-[18%] h-72 w-72" style={ { animationDelay: '1.6s' } } />
        <Card className="panel-card elevated relative z-10 w-full max-w-md gap-0 py-0">
          <CardContent className="px-6 py-9">
            <div className="flex flex-col items-center gap-5 text-center">
              <div className="flex size-16 items-center justify-center rounded-2xl bg-[color-mix(in_oklab,var(--primary)_12%,var(--card))] text-primary shadow-[0_8px_24px_-8px_rgb(var(--primary-rgb)/0.5)]">
                <LoaderCircle className="size-7 animate-spin" />
              </div>
              <div className="space-y-2">
                <h2 className="text-2xl font-bold tracking-tight">
                  <span className="text-gradient-brand">控制台启动中</span>
                </h2>
                <p className="mx-auto max-w-sm text-[14px] leading-6 text-muted-foreground">
                  正在建立管理面认证状态、模型索引、账号池快照和会话流订阅。
                </p>
              </div>
            </div>
          </CardContent>
        </Card>
      </main>
    );
  }

  return (
    <main className="console-surface">
      <div className="flex min-h-screen min-w-0">
        <AdminSidebar
          activeTab={activeTab}
          onTabChange={setActiveTab}
          authState={authState}
          defaultModel={defaultModel}
          spaceName={configPayload?.session?.space_name || accountsPayload?.session?.space_name}
          activeAccount={accountsPayload?.active_account}
          onLogout={() => void logout()}
          mobileOpen={mobileNavOpen}
          onMobileOpenChange={setMobileNavOpen}
        />

        <section className="min-w-0 flex-1">
          <header className="topbar-shell sticky top-0 z-20 border-b">
            <div
              className="topbar-inner py-3"
              data-fullwidth={isFullWidth ? 'true' : 'false'}
              style={ { maxWidth: isFullWidth ? '100%' : 'var(--content-max-width)' } }
            >
              <Button
                variant="outline"
                size="icon"
                className="lg:hidden"
                onClick={() => setMobileNavOpen(true)}
              >
                <Menu className="size-4" />
              </Button>

              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2.5">
                  <h2 className="text-base font-semibold tracking-tight">{TAB_LABEL[activeTab]}</h2>
                  <span className="topbar-dot" aria-hidden />
                  <span className="text-xs text-muted-foreground">Nation2API 管理台</span>
                </div>
                <div className="mt-1 hidden items-center gap-2 text-xs text-muted-foreground md:flex">
                  <Badge variant="secondary" className="px-2 py-0.5 text-[11px]">{authState}</Badge>
                  <span className="truncate">Stream · {streamState}</span>
                </div>
              </div>

              <div className="ml-auto flex w-full flex-wrap items-center justify-end gap-2.5 lg:w-auto">
                <div className="hidden xl:flex max-w-[260px]">
                  <div className="status-chip" title={accountsPayload?.active_account || '—'}>
                    <span className="size-1.5 rounded-full bg-[linear-gradient(135deg,#10B981,#059669)]" />
                    <span className="truncate">{accountsPayload?.active_account || '未指定账号'}</span>
                  </div>
                </div>
                <Select value={themeMode} onValueChange={(value) => setTheme(value)}>
                  <SelectTrigger size="sm" className="min-w-[128px] md:w-[140px]">
                    <SelectValue placeholder="主题模式" />
                  </SelectTrigger>
                  <SelectContent align="end">
                    <SelectItem value="system">
                      <span className="flex items-center gap-2">
                        <Monitor className="size-4 text-muted-foreground" />
                        跟随系统
                      </span>
                    </SelectItem>
                    <SelectItem value="light">
                      <span className="flex items-center gap-2">
                        <Sun className="size-4 text-muted-foreground" />
                        浅色
                      </span>
                    </SelectItem>
                    <SelectItem value="dark">
                      <span className="flex items-center gap-2">
                        <Moon className="size-4 text-muted-foreground" />
                        深色
                      </span>
                    </SelectItem>
                  </SelectContent>
                </Select>
                <Select value={accentTheme} onValueChange={(value) => setAccentTheme(value as typeof accentTheme)}>
                  <SelectTrigger size="sm" className="min-w-[128px] md:w-[140px]">
                    <SelectValue placeholder="主题色" />
                  </SelectTrigger>
                  <SelectContent align="end">
                    {ACCENT_THEME_OPTIONS.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        <span className="flex items-center gap-2">
                          <span
                            className="size-2.5 rounded-full ring-1 ring-black/5"
                            style={ { background: option.preview } }
                          />
                          <span>{option.label}</span>
                        </span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Button
                  variant="ghost"
                  size="icon"
                  className="hidden md:inline-flex"
                  onClick={() => setIsFullWidth((current) => !current)}
                  aria-label={isFullWidth ? '退出全宽' : '进入全宽'}
                >
                  {isFullWidth ? <Minimize2 className="size-4" /> : <Maximize2 className="size-4" />}
                </Button>
                <Button onClick={() => void refreshAll()}>
                  <RefreshCcw className="size-4" />
                  重新同步
                </Button>
              </div>
            </div>
          </header>

          <div className="min-w-0 overflow-x-hidden">
            <div className="console-content" data-fullwidth={isFullWidth ? 'true' : 'false'}>
              {bootError ? (
                <div className="mb-6 flex items-start gap-3 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm leading-6 text-destructive">
                  <AlertTriangle className="mt-0.5 size-4 shrink-0" />
                  <div>
                    <span className="font-semibold">加载异常：</span>
                    {bootError}
                  </div>
                </div>
              ) : null}

              {authenticated ? renderActivePanel() : null}
            </div>
          </div>
        </section>
      </div>

      {shouldShowOverlay ? (
        <LoginOverlay
          passwordConfigured={passwordConfigured}
          message={
            loginMessage ||
            bootError ||
            (passwordConfigured ? '请输入密码登录。' : '未检测到 admin.password。')
          }
          busy={loginBusy}
          onSubmit={async (password) => {
            setLoginBusy(true);
            setLoginMessage('');
            try {
              await login(password);
              setLoginMessage('');
            } catch (error) {
              const message = error instanceof Error ? error.message : '登录失败';
              setLoginMessage(message);
              throw error;
            } finally {
              setLoginBusy(false);
            }
          }}
        />
      ) : null}
    </main>
  );
}
