'use client';

import { useEffect, useMemo, useState } from 'react';
import { DatabaseZap, Layers3, Sparkles } from 'lucide-react';
import { EmptyHint, InfoCard, KeyValueGrid, MetaTile, PanelHeader, StatCard } from '@/components/admin/shared';
import { Badge } from '@/components/ui/badge';
import { ScrollArea } from '@/components/ui/scroll-area';
import type { ModelItem } from '@/lib/services/admin/types';
import { cn } from '@/lib/utils';

function uniqueValues(items: Array<string | undefined>) {
  return [...new Set(items.filter(Boolean))];
}

export function ModelsPanel({
  models,
  defaultModel,
}: {
  models: ModelItem[];
  defaultModel?: string;
}) {
  const [selectedModelId, setSelectedModelId] = useState(defaultModel || models[0]?.id || '');

  useEffect(() => {
    const fallback = defaultModel || models[0]?.id || '';
    setSelectedModelId((current) => (current && models.some((item) => item.id === current) ? current : fallback));
  }, [defaultModel, models]);

  const families = uniqueValues(models.map((item) => item.family));
  const groups = uniqueValues(models.map((item) => item.group));
  const betaCount = models.filter((item) => item.beta).length;
  const enabledCount = models.filter((item) => item.enabled !== false).length;
  const selectedModel = useMemo(
    () => models.find((item) => item.id === selectedModelId) || null,
    [models, selectedModelId],
  );

  return (
    <div className="space-y-6">
      <PanelHeader
        eyebrow="Models"
        title="模型注册表"
        description="查看模型映射、状态和内部目标。"
      />

      <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-4">
        <StatCard label="模型总数" value={String(models.length)} hint="public IDs" />
        <StatCard label="默认模型" value={defaultModel || '-'} hint="当前配置" />
        <StatCard label="Family 数" value={String(families.length)} hint={families.join(' · ') || '-'} />
        <StatCard label="Enabled / Beta" value={`${enabledCount} / ${betaCount}`} hint={groups.join(' · ') || '无分组'} />
      </div>

      <div className="grid gap-6 xl:grid-cols-[minmax(320px,360px)_minmax(0,1fr)]">
        <InfoCard title="模型列表" description={`共 ${models.length} 个模型，选择后查看映射详情。`}>
          {models.length ? (
            <ScrollArea className="console-list-scroll pretty-scroll pr-3">
              <div className="space-y-3 pb-1">
                {models.map((model) => {
                  const selected = model.id === selectedModelId;
                  return (
                    <button
                      key={model.id}
                      type="button"
                      onClick={() => setSelectedModelId(model.id)}
                      className={cn(
                        'w-full rounded-lg border px-4 py-4 text-left transition-all',
                        selected
                          ? 'border-primary/40 bg-[color-mix(in_oklab,var(--primary)_10%,var(--card))] shadow-soft'
                          : 'border-border/70 bg-card hover:border-primary/20 hover:bg-muted/40',
                      )}
                    >
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0 space-y-1">
                          <div className="break-all text-sm font-semibold">{model.id}</div>
                          <div className="text-sm text-muted-foreground">{model.name || '未命名模型'}</div>
                        </div>
                        <div className="flex flex-wrap justify-end gap-2">
                          <Badge className="normal-case tracking-normal">{model.enabled === false ? 'disabled' : 'enabled'}</Badge>
                          {model.beta ? <Badge variant="secondary" className="normal-case tracking-normal">beta</Badge> : null}
                        </div>
                      </div>
                      <div className="mt-3 grid gap-2 text-xs leading-5 text-muted-foreground sm:grid-cols-2">
                        <div>family · {model.family || '-'}</div>
                        <div>group · {model.group || '-'}</div>
                        <div className="sm:col-span-2 break-all">notion · {model.notion_model || '-'}</div>
                      </div>
                    </button>
                  );
                })}
              </div>
            </ScrollArea>
          ) : (
            <EmptyHint title="还没有模型" description="请先确认 `/admin/config` 是否返回了 `models` 字段。" />
          )}
        </InfoCard>

        {selectedModel ? (
          <div className="min-w-0 space-y-6">
            <InfoCard
              title={selectedModel.name || selectedModel.id}
              description="当前路由模型的映射信息。"
              actions={
                <div className="flex flex-wrap items-center gap-2">
                  <Badge className="normal-case tracking-normal px-3 py-1.5">
                    <DatabaseZap className="size-3.5" />
                    {selectedModel.enabled === false ? 'disabled' : 'enabled'}
                  </Badge>
                  {selectedModel.beta ? (
                    <Badge variant="secondary" className="normal-case tracking-normal px-3 py-1.5">
                      <Sparkles className="size-3.5" />
                      beta
                    </Badge>
                  ) : null}
                  {selectedModel.group ? (
                    <Badge variant="outline" className="normal-case tracking-normal px-3 py-1.5">
                      <Layers3 className="size-3.5" />
                      {selectedModel.group}
                    </Badge>
                  ) : null}
                </div>
              }
            >
              <KeyValueGrid
                items={[
                  { label: 'Public ID', value: selectedModel.id },
                  { label: '显示名称', value: selectedModel.name || '-' },
                  { label: 'Family', value: selectedModel.family || '-' },
                  { label: 'Group', value: selectedModel.group || '-' },
                  { label: 'Notion Model', value: selectedModel.notion_model || '-' },
                  { label: '默认模型', value: selectedModel.id === defaultModel ? '是' : '否' },
                ]}
              />
            </InfoCard>

            <InfoCard title="映射详情" description="用于调试与授权路由的详细状态。">
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                <MetaTile label="路由 ID" scrollable value={selectedModel.id} />
                <MetaTile label="内部模型" scrollable value={selectedModel.notion_model || '-'} />
                <MetaTile label="归类" scrollable value={[selectedModel.family || '-', selectedModel.group || '-'].join(' · ')} />
              </div>
            </InfoCard>
          </div>
        ) : (
          <EmptyHint title="请选择一个模型" description="选择后查看映射详情。" />
        )}
      </div>
    </div>
  );
}
