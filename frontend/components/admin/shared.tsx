import type { ReactNode } from 'react';
import { cn, formatDateTime } from '@/lib/utils';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { ScrollArea } from '@/components/ui/scroll-area';
import { CopyIcon, type LucideIcon } from 'lucide-react';
import type { ConversationMessageAttachment } from '@/lib/services/admin/types';

export function PanelHeader({
  eyebrow,
  title,
  description,
  actions,
}: {
  eyebrow: string;
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between min-w-0">
      <div className="min-w-0 space-y-2.5">
        <p className="section-eyebrow">{eyebrow}</p>
        <h1 className="text-2xl font-bold leading-[1.15] tracking-tight md:text-[34px]">
          <span className="text-gradient-brand">{title}</span>
        </h1>
        {description ? (
          <p className="max-w-2xl text-[15px] leading-7 text-muted-foreground">
            {description}
          </p>
        ) : null}
      </div>
      {actions ? (
        <div className="flex flex-wrap items-center gap-2.5 lg:flex-nowrap lg:justify-end">
          {actions}
        </div>
      ) : null}
    </div>
  );
}

export function StatCard({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <Card className="panel-card group relative gap-2 overflow-hidden py-0 transition-transform duration-200 hover:-translate-y-[2px]">
      <span
        aria-hidden
        className="pointer-events-none absolute -right-10 -top-10 size-32 rounded-full bg-[radial-gradient(circle_at_center,rgb(var(--primary-rgb)/0.18),transparent_70%)] opacity-60 transition-opacity duration-200 group-hover:opacity-100"
      />
      <CardContent className="relative space-y-2.5 px-5 py-5">
        <div className="text-[11px] font-bold uppercase tracking-[0.16em] text-muted-foreground">
          {label}
        </div>
        <div className="metric-value">{value || '—'}</div>
        {hint ? (
          <div className="text-[13px] leading-5 text-muted-foreground line-clamp-2">{hint}</div>
        ) : null}
      </CardContent>
    </Card>
  );
}

export function InfoCard({
  title,
  description,
  children,
  actions,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <Card className={cn('panel-card min-w-0 gap-3 py-0', className)}>
      <CardHeader className="border-b px-5 pb-4 pt-5">
        <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between min-w-0">
          <div className="min-w-0 space-y-1.5">
            <CardTitle className="text-[16px] font-semibold tracking-tight md:text-[17px]">{title}</CardTitle>
            {description ? (
              <CardDescription className="max-w-2xl text-[13.5px] leading-6">{description}</CardDescription>
            ) : null}
          </div>
          {actions ? (
            <div className="flex flex-wrap items-center gap-2 lg:flex-nowrap lg:justify-end">
              {actions}
            </div>
          ) : null}
        </div>
      </CardHeader>
      <CardContent className="px-5 py-5">{children}</CardContent>
    </Card>
  );
}

export function KeyValueGrid({ items }: { items: Array<{ label: string; value?: string | number | null }> }) {
  return (
    <div className="grid gap-3 md:grid-cols-2 2xl:grid-cols-3">
      {items.map((item) => (
        <div key={item.label} className="surface-subtle min-w-0 px-3.5 py-3">
          <div className="text-[10.5px] font-bold uppercase tracking-[0.14em] text-muted-foreground">
            {item.label}
          </div>
          <div className="mt-2 value-box pretty-scroll">{String(item.value ?? '—')}</div>
        </div>
      ))}
    </div>
  );
}

export function JsonPreview({
  title,
  value,
  onCopy,
  minHeight = 260,
}: {
  title: string;
  value: string;
  onCopy?: () => void;
  minHeight?: number;
}) {
  return (
    <InfoCard
      title={title}
      actions={
        onCopy ? (
          <Button variant="outline" size="sm" onClick={onCopy}>
            <CopyIcon className="size-4" />
            复制
          </Button>
        ) : null
      }
    >
      <ScrollArea
        className="code-surface pretty-scroll overflow-hidden border"
        style={ { minHeight, maxHeight: 520 } }
      >
        <pre className="min-h-full whitespace-pre-wrap px-4 py-3 font-mono text-[12px] leading-6">{value}</pre>
      </ScrollArea>
    </InfoCard>
  );
}

export function StatusPill({ status }: { status?: string }) {
  const normalized = (status || 'unknown').toLowerCase();
  const variant: 'default' | 'secondary' | 'destructive' | 'outline' | 'soft' | 'success' =
    normalized.includes('fail') || normalized.includes('error')
      ? 'destructive'
      : normalized.includes('pending') || normalized.includes('start') || normalized.includes('new') || normalized.includes('running')
        ? 'soft'
        : normalized.includes('ok') || normalized.includes('ready') || normalized.includes('success') || normalized.includes('complete')
          ? 'success'
          : 'secondary';
  return <Badge variant={variant} className="normal-case">{status || 'unknown'}</Badge>;
}

export function FileChips({ items }: { items?: ConversationMessageAttachment[] }) {
  if (!items?.length) return null;
  return (
    <div className="flex flex-wrap gap-2">
      {items.map((item, index) => {
        const label = [item.name || 'attachment', item.content_type || item.contentType || ''].filter(Boolean).join(' · ');
        return (
          <Badge key={`${label}-${index}`} variant="secondary" className="normal-case tracking-normal">
            {label}
          </Badge>
        );
      })}
    </div>
  );
}

export function EmptyHint({ title, description }: { title: string; description: string }) {
  return (
    <div className="surface-subtle flex flex-col items-center justify-center gap-2 px-6 py-10 text-center">
      <div
        aria-hidden
        className="flex size-12 items-center justify-center rounded-full bg-[color-mix(in_oklab,var(--primary)_12%,var(--card))] text-primary"
      >
        <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <circle cx="12" cy="12" r="9" />
          <path d="M12 8v5" />
          <path d="M12 16h.01" />
        </svg>
      </div>
      <h3 className="text-[15px] font-semibold tracking-tight">{title}</h3>
      <p className="mx-auto max-w-md text-[13.5px] leading-6 text-muted-foreground">{description}</p>
    </div>
  );
}

export function formatMaybeDate(value?: string) {
  return value ? formatDateTime(value) : '—';
}

export function Subsection({
  eyebrow,
  title,
  description,
  icon: Icon,
  actions,
  className,
  children,
}: {
  eyebrow: string;
  title: string;
  description?: string;
  icon?: LucideIcon;
  actions?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={cn('surface-subtle min-w-0 p-5', className)}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-3">
          {Icon ? (
            <div className="flex size-9 shrink-0 items-center justify-center rounded-lg border border-primary/15 bg-[color-mix(in_oklab,var(--primary)_10%,var(--card))] text-primary">
              <Icon className="size-[18px]" />
            </div>
          ) : null}
          <div className="min-w-0 space-y-1">
            <p className="section-eyebrow">{eyebrow}</p>
            <h3 className="text-[15px] font-semibold tracking-tight">{title}</h3>
            {description ? (
              <p className="text-[13px] leading-6 text-muted-foreground">{description}</p>
            ) : null}
          </div>
        </div>
        {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
      </div>
      <div className="mt-5">{children}</div>
    </div>
  );
}

export function MetaTile({
  label,
  value,
  className,
  scrollable = false,
}: {
  label: string;
  value: ReactNode;
  className?: string;
  scrollable?: boolean;
}) {
  return (
    <div className={cn('surface-subtle min-w-0 px-4 py-4', className)}>
      <div className="section-eyebrow">{label}</div>
      <div className={cn('mt-2 text-sm font-medium break-all', scrollable && 'value-box pretty-scroll')}>
        {value}
      </div>
    </div>
  );
}
