import type { AccountsPayload, AdminConfigPayload, HealthPayload, VersionPayload } from '@/lib/services/admin/types';
import { CheckCircle2, ShieldCheck, XCircle } from 'lucide-react';
import { InfoCard, KeyValueGrid, PanelHeader, StatCard } from '@/components/admin/shared';
import { Badge } from '@/components/ui/badge';

function featureBadge(value: boolean | string, trueLabel = '开启', falseLabel = '关闭') {
  if (typeof value === 'boolean') {
    return value ? (
      <Badge variant="success" className="gap-1">
        <CheckCircle2 className="size-3" />
        {trueLabel}
      </Badge>
    ) : (
      <Badge variant="outline" className="gap-1 text-muted-foreground">
        <XCircle className="size-3" />
        {falseLabel}
      </Badge>
    );
  }
  return <Badge variant="secondary" className="font-mono normal-case">{value}</Badge>;
}

export function DashboardPanel({
  configPayload,
  versionPayload,
  healthPayload,
  accountsPayload,
}: {
  configPayload: AdminConfigPayload | null;
  versionPayload: VersionPayload | null;
  healthPayload: HealthPayload | null;
  accountsPayload: AccountsPayload | null;
}) {
  const session = configPayload?.session;
  const runtime = configPayload?.session_refresh_runtime;
  const models = configPayload?.models || [];
  const features = configPayload?.config?.features || {};
  const featureLines: Array<[string, boolean | string]> = [
    ['默认联网', Boolean(features.use_web_search)],
    ['只读模式', Boolean(features.use_read_only_mode)],
    ['强制关闭“可以进行更改”', features.force_disable_upstream_edits !== false],
    ['Writer Mode', Boolean(features.writer_mode)],
    ['图片生成', Boolean(features.enable_generate_image)],
    ['CSV 附件', Boolean(features.enable_csv_attachment_support)],
    ['AI Surface', String(features.ai_surface || 'ai_module')],
    ['Thread Type', String(features.thread_type || 'workflow')],
  ];

  const healthLabel = healthPayload?.session_ready ? 'READY' : healthPayload?.ok ? 'NO SESSION' : 'UNKNOWN';
  const sessionReady = Boolean(accountsPayload?.session_ready);

  return (
    <div className="space-y-7">
      <PanelHeader
        eyebrow="Dashboard"
        title="运行状态总览"
        description="查看服务健康、会话状态与刷新运行态的快照。"
      />

      <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-4">
        <StatCard label="健康状态" value={healthLabel} hint={versionPayload?.version || '—'} />
        <StatCard label="模型数量" value={String(models.length)} hint={[...new Set(models.map((item) => item.family).filter(Boolean))].join(' · ') || '—'} />
        <StatCard label="当前用户" value={session?.user_email || '—'} hint={session?.space_name || '—'} />
        <StatCard label="活跃账号" value={accountsPayload?.active_account || '—'} hint={sessionReady ? 'session ready' : 'session not ready'} />
      </div>

      <div className="grid gap-6 2xl:grid-cols-[minmax(0,1.05fr)_360px]">
        <div className="min-w-0 space-y-6">
          <InfoCard title="当前协议会话" description="服务端实时快照。">
            <KeyValueGrid
              items={[
                { label: 'Probe', value: session?.probe_path },
                { label: 'Client Version', value: session?.client_version },
                { label: 'User ID', value: session?.user_id },
                { label: 'Space ID', value: session?.space_id },
                { label: 'User Name', value: session?.user_name },
                { label: 'Workspace', value: session?.space_name },
              ]}
            />
          </InfoCard>

          <InfoCard title="默认能力开关" description="当前默认能力位，可在设置面调整。">
            <div className="grid gap-2.5 sm:grid-cols-2 xl:grid-cols-2">
              {featureLines.map(([label, value]) => (
                <div
                  key={label}
                  className="surface-subtle flex items-center justify-between gap-3 px-3.5 py-3"
                >
                  <div className="min-w-0 text-[12.5px] font-semibold leading-5">{label}</div>
                  <div className="shrink-0">{featureBadge(value)}</div>
                </div>
              ))}
            </div>
          </InfoCard>
        </div>

        <div className="space-y-6 self-start xl:sticky xl:top-6">
          <InfoCard title="刷新运行态" description="最近刷新、错误与版本。">
            <div className="grid gap-2.5">
              {[
                ['Last Refresh', runtime?.last_refresh_at || '—'],
                ['Refresh Error', runtime?.last_error || '—'],
                ['Session Ready', sessionReady ? 'yes' : 'no'],
                ['Version', versionPayload?.version || '—'],
              ].map(([label, value]) => (
                <div key={label} className="surface-subtle px-3.5 py-3">
                  <div className="text-[10.5px] font-bold uppercase tracking-[0.14em] text-muted-foreground">{label}</div>
                  <div className="mt-1.5 value-box pretty-scroll">{value}</div>
                </div>
              ))}
            </div>
          </InfoCard>

          <InfoCard title="部署提示" description="上线前优先核对。">
            <div className="flex items-start gap-3 rounded-xl border border-dashed border-[color-mix(in_oklab,var(--primary)_28%,transparent)] bg-[color-mix(in_oklab,var(--primary)_5%,var(--card))] px-4 py-3.5 text-[13.5px] leading-6 text-muted-foreground">
                <ShieldCheck className="mt-0.5 size-4 shrink-0 text-primary" />
                <p>
                  建议持久化 SQLite、会话目录和管理配置；若 <code className="rounded bg-muted px-1 py-0.5 font-mono text-[12px]">session ready</code> 长期为 <code className="rounded bg-muted px-1 py-0.5 font-mono text-[12px]">no</code>，先检查活跃账号与最近刷新错误。
                </p>
              </div>
          </InfoCard>
        </div>
      </div>
    </div>
  );
}
