'use client';

import type { ComponentType } from 'react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog';
import type { TabKey } from '@/lib/services/admin/types';
import { Braces, History, KeyRound, LayoutDashboard, LogOut, Settings2, Sparkles } from 'lucide-react';
import { cn } from '@/lib/utils';

const tabMeta: Array<{ key: TabKey; label: string; icon: ComponentType<{ className?: string }>; group: 'workbench' | 'config' }> = [
  { key: 'dashboard', label: '状态', icon: LayoutDashboard, group: 'workbench' },
  { key: 'tester', label: 'API Tester', icon: Sparkles, group: 'workbench' },
  { key: 'conversations', label: '会话', icon: History, group: 'workbench' },
  { key: 'settings', label: '设置', icon: Settings2, group: 'config' },
  { key: 'accounts', label: '账号', icon: KeyRound, group: 'config' },
  { key: 'models', label: '模型', icon: Braces, group: 'config' },
];

function NavButton({
  active,
  Icon,
  label,
  onClick,
}: {
  active: boolean;
  Icon: ComponentType<{ className?: string }>;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'group relative flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left text-[13.5px] font-medium transition-all duration-200',
        active
          ? 'bg-[linear-gradient(135deg,rgb(var(--primary-rgb)/0.16),rgb(var(--secondary-rgb)/0.10))] text-foreground shadow-[0_4px_14px_-6px_rgb(var(--primary-rgb)/0.45)]'
          : 'text-muted-foreground hover:bg-muted hover:text-foreground',
      )}
    >
      {active ? (
        <span
          aria-hidden
          className="absolute left-0 top-1/2 h-5 w-[3px] -translate-y-1/2 rounded-full bg-[linear-gradient(180deg,#4F46E5,#7C3AED)]"
        />
      ) : null}
      <Icon className={cn('size-[17px] shrink-0 transition-colors', active && 'text-primary')} />
      <span className="truncate">{label}</span>
    </button>
  );
}

function SidebarContent({
  activeTab,
  onTabChange,
  authState,
  defaultModel,
  spaceName,
  activeAccount,
  onLogout,
  onNavigate,
}: {
  activeTab: TabKey;
  onTabChange: (tab: TabKey) => void;
  authState: string;
  defaultModel?: string;
  spaceName?: string;
  activeAccount?: string;
  onLogout: () => void;
  onNavigate?: () => void;
}) {
  const runtimeItems: Array<[string, string]> = [
    ['默认模型', defaultModel || '—'],
    ['当前空间', spaceName || '—'],
    ['活跃账号', activeAccount || '—'],
  ];

  const workbench = tabMeta.filter((item) => item.group === 'workbench');
  const config = tabMeta.filter((item) => item.group === 'config');

  return (
    <div className="sidebar-scroll pretty-scroll flex h-full min-w-0 flex-col gap-6 overflow-y-auto px-4 py-5">
      {/* Brand */}
      <div className="flex items-center gap-3">
        <div className="brand-badge size-10 text-base tracking-tight">N</div>
        <div className="min-w-0 space-y-0.5">
          <div className="text-[15px] font-bold leading-tight tracking-tight">Nation2API</div>
          <div className="text-[11.5px] font-medium uppercase tracking-[0.15em] text-muted-foreground">
            Enterprise Console
          </div>
        </div>
      </div>

      {/* Auth status pill */}
      <div className="surface-tinted flex items-center justify-between gap-2 px-3.5 py-2.5">
        <div className="min-w-0">
          <div className="text-[10.5px] font-bold uppercase tracking-[0.14em] text-muted-foreground">认证状态</div>
          <div className="mt-0.5 truncate text-[13px] font-semibold">{authState}</div>
        </div>
        <span className="size-2 rounded-full bg-[linear-gradient(135deg,#10B981,#059669)] shadow-[0_0_10px_rgba(16,185,129,0.6)]" />
      </div>

      {/* Nav — workbench */}
      <div className="space-y-2.5">
        <div className="px-1 text-[10.5px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
          工作台
        </div>
        <nav className="grid gap-1">
          {workbench.map((item) => (
            <NavButton
              key={item.key}
              active={activeTab === item.key}
              Icon={item.icon}
              label={item.label}
              onClick={() => {
                onTabChange(item.key);
                onNavigate?.();
              }}
            />
          ))}
        </nav>
      </div>

      {/* Nav — config */}
      <div className="space-y-2.5">
        <div className="px-1 text-[10.5px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
          配置
        </div>
        <nav className="grid gap-1">
          {config.map((item) => (
            <NavButton
              key={item.key}
              active={activeTab === item.key}
              Icon={item.icon}
              label={item.label}
              onClick={() => {
                onTabChange(item.key);
                onNavigate?.();
              }}
            />
          ))}
        </nav>
      </div>

      {/* Runtime context */}
      <div className="space-y-2.5">
        <div className="px-1 text-[10.5px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
          运行上下文
        </div>
        <div className="surface-subtle space-y-2 px-3.5 py-3">
          {runtimeItems.map(([label, value]) => (
            <div key={label} className="min-w-0">
              <div className="text-[10.5px] font-bold uppercase tracking-[0.14em] text-muted-foreground">{label}</div>
              <div className="mt-1 truncate text-[12.5px] font-medium text-foreground/90" title={value}>
                {value}
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* Footer */}
      <div className="mt-auto space-y-3 pt-2">
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            className="flex-1 justify-center"
            onClick={() => {
              onLogout();
              onNavigate?.();
            }}
          >
            <LogOut className="size-4" />
            退出控制台
          </Button>
          <Badge variant="secondary" className="px-2.5 py-1">admin</Badge>
        </div>
      </div>
    </div>
  );
}

export function AdminSidebar({
  activeTab,
  onTabChange,
  authState,
  defaultModel,
  spaceName,
  activeAccount,
  onLogout,
  mobileOpen,
  onMobileOpenChange,
}: {
  activeTab: TabKey;
  onTabChange: (tab: TabKey) => void;
  authState: string;
  defaultModel?: string;
  spaceName?: string;
  activeAccount?: string;
  onLogout: () => void;
  mobileOpen: boolean;
  onMobileOpenChange: (open: boolean) => void;
}) {
  return (
    <>
      <aside className="sidebar-shell hidden w-[268px] shrink-0 border-r lg:sticky lg:top-0 lg:block lg:h-screen">
        <SidebarContent
          activeTab={activeTab}
          onTabChange={onTabChange}
          authState={authState}
          defaultModel={defaultModel}
          spaceName={spaceName}
          activeAccount={activeAccount}
          onLogout={onLogout}
        />
      </aside>

      <Dialog open={mobileOpen} onOpenChange={onMobileOpenChange}>
        <DialogContent
          showCloseButton
          className="sidebar-shell lg:hidden !left-0 !top-0 !h-screen !w-[min(92vw,300px)] !max-w-[300px] !translate-x-0 !translate-y-0 rounded-none border-0 border-r p-0"
        >
          <DialogTitle className="sr-only">导航菜单</DialogTitle>
          <SidebarContent
            activeTab={activeTab}
            onTabChange={onTabChange}
            authState={authState}
            defaultModel={defaultModel}
            spaceName={spaceName}
            activeAccount={activeAccount}
            onLogout={onLogout}
            onNavigate={() => onMobileOpenChange(false)}
          />
        </DialogContent>
      </Dialog>
    </>
  );
}
