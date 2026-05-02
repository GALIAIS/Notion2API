'use client';

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { toast } from 'sonner';
import { RefreshCcw, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import {
  EmptyHint,
  FileChips,
  InfoCard,
  KeyValueGrid,
  MetaTile,
  PanelHeader,
  StatCard,
  StatusPill,
  formatMaybeDate,
} from '@/components/admin/shared';
import type { ConversationDetail, ConversationSummary } from '@/lib/services/admin/types';
import { cn } from '@/lib/utils';

const ACCOUNT_FILTER_ALL = '__all__';
const ACCOUNT_FILTER_UNKNOWN = '__unknown__';

function conversationOriginLabel(origin?: string) {
  switch (origin) {
    case 'merged':
      return '本地 + Notion';
    case 'notion':
      return 'Notion';
    case 'local':
    default:
      return '本地';
  }
}

export function ConversationsPanel({
  conversations,
  selectedConversationId,
  selectedConversation,
  streamState,
  onRefresh,
  onSelect,
  onDelete,
  onBatchDelete,
}: {
  conversations: ConversationSummary[];
  selectedConversationId: string;
  selectedConversation: ConversationDetail | ConversationSummary | null;
  streamState: string;
  onRefresh: () => Promise<unknown>;
  onSelect: (id: string) => Promise<unknown>;
  onDelete: (id: string) => Promise<unknown>;
  onBatchDelete: (ids: string[]) => Promise<unknown>;
}) {
  const [selectedIDs, setSelectedIDs] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [accountFilter, setAccountFilter] = useState(ACCOUNT_FILTER_ALL);

  useEffect(() => {
    setSelectedIDs((current) => current.filter((id) => conversations.some((item) => item.id === id)));
  }, [conversations]);

  const accountOptions = useMemo(
    () =>
      [...new Set(conversations.map((item) => item.account_email?.trim()).filter((value): value is string => Boolean(value)))].sort(
        (left, right) => left.localeCompare(right),
      ),
    [conversations],
  );

  const filteredConversations = useMemo(
    () =>
      conversations.filter((item) => {
        const accountEmail = item.account_email?.trim();
        if (accountFilter === ACCOUNT_FILTER_ALL) return true;
        if (accountFilter === ACCOUNT_FILTER_UNKNOWN) return !accountEmail;
        return accountEmail === accountFilter;
      }),
    [accountFilter, conversations],
  );

  useEffect(() => {
    setSelectedIDs((current) => current.filter((id) => filteredConversations.some((item) => item.id === id)));
  }, [filteredConversations]);

  useEffect(() => {
    if (!filteredConversations.length) return;
    if (filteredConversations.some((item) => item.id === selectedConversationId)) return;
    void onSelect(filteredConversations[0].id);
  }, [filteredConversations, onSelect, selectedConversationId]);

  const activeConversation = useMemo(
    () =>
      selectedConversation && filteredConversations.some((item) => item.id === selectedConversation.id)
        ? selectedConversation
        : null,
    [filteredConversations, selectedConversation],
  );

  const detailedConversation = useMemo(
    () => (activeConversation && 'messages' in activeConversation ? (activeConversation as ConversationDetail) : null),
    [activeConversation],
  );

  const conversationMessages = useMemo(() => detailedConversation?.messages || [], [detailedConversation]);
  const selectedItems = useMemo(
    () => filteredConversations.filter((item) => selectedIDs.includes(item.id)),
    [filteredConversations, selectedIDs],
  );
  const selectedLocalCount = selectedItems.filter((item) => item.origin !== 'notion').length;
  const selectedRemoteCount = selectedItems.filter((item) => item.origin !== 'local').length;
  const hasRunningSelection = selectedItems.some((item) => item.status === 'running');
  const localCount = filteredConversations.filter((item) => item.origin === 'local').length;
  const mergedCount = filteredConversations.filter((item) => item.origin === 'merged').length;
  const remoteCount = filteredConversations.filter((item) => item.origin === 'notion').length;
  const allSelected = filteredConversations.length > 0 && selectedIDs.length === filteredConversations.length;
  const activeFilterLabel =
    accountFilter === ACCOUNT_FILTER_ALL ? '全部账号' : accountFilter === ACCOUNT_FILTER_UNKNOWN ? '未记录账号' : accountFilter;

  const summaryCards = [
    { label: '会话总数', value: String(filteredConversations.length), hint: activeFilterLabel },
    { label: '来源分布', value: `${localCount} / ${mergedCount} / ${remoteCount}`, hint: '本地 / 合并 / Notion' },
    { label: '当前状态', value: activeConversation?.status || '-', hint: conversationOriginLabel(activeConversation?.origin) },
    { label: '消息数', value: String(conversationMessages.length), hint: '当前选中会话' },
  ];

  const toggleConversation = (id: string) => {
    setSelectedIDs((current) => (current.includes(id) ? current.filter((item) => item !== id) : [...current, id]));
  };

  const toggleSelectAll = () => {
    setSelectedIDs((current) => (current.length === filteredConversations.length ? [] : filteredConversations.map((item) => item.id)));
  };

  const handleSingleDelete = async (id: string) => {
    setBusy(true);
    try {
      await onDelete(id);
      setSelectedIDs((current) => current.filter((item) => item !== id));
      toast.success('会话已删除，并已同步到 Notion。');
    } catch (error) {
      toast.error(error instanceof Error ? error.message : '删除会话失败');
    } finally {
      setBusy(false);
    }
  };

  const handleBatchDelete = async () => {
    if (!selectedIDs.length) return;
    setBusy(true);
    try {
      await onBatchDelete(selectedIDs);
      setSelectedIDs([]);
      toast.success(`已删除 ${selectedIDs.length} 条会话，本地与 Notion 已同步。`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : '批量删除失败');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-6">
      <PanelHeader
        eyebrow="Conversations"
        title="会话管理与实时流"
        description="查看最近线程，按账号筛选，并同步删除远端会话。"
        actions={
          <>
            <div className="status-chip text-sm">{streamState}</div>
            <Button variant="outline" disabled={busy} onClick={() => void onRefresh()}>
              <RefreshCcw className="size-4" />
              刷新会话
            </Button>
          </>
        }
      />

      <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-4">
        {summaryCards.map((item) => (
          <StatCard key={item.label} label={item.label} value={item.value} hint={item.hint} />
        ))}
      </div>

      <div className="grid gap-6 xl:grid-cols-[minmax(340px,380px)_minmax(0,1fr)]">
        <InfoCard
          title="最近会话"
          description="按更新时间排序，支持账号筛选、批量删除和远端同步。"
          actions={
            conversations.length ? (
              <div className="flex flex-wrap items-center gap-2">
                <Select value={accountFilter} onValueChange={setAccountFilter}>
                  <SelectTrigger size="sm" className="min-w-[176px] bg-background/80">
                    <SelectValue placeholder="筛选账号" />
                  </SelectTrigger>
                  <SelectContent align="end">
                    <SelectItem value={ACCOUNT_FILTER_ALL}>全部账号</SelectItem>
                    <SelectItem value={ACCOUNT_FILTER_UNKNOWN}>未记录账号</SelectItem>
                    {accountOptions.map((email) => (
                      <SelectItem key={email} value={email}>
                        {email}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Button variant="outline" size="sm" disabled={busy} onClick={toggleSelectAll}>
                  {allSelected ? '取消全选' : '全选'}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  className="text-destructive hover:text-destructive"
                  disabled={busy || !selectedIDs.length || hasRunningSelection}
                  onClick={() => void handleBatchDelete()}
                >
                  <Trash2 className="size-4" />
                  批量删除 {selectedIDs.length ? `(${selectedIDs.length})` : ''}
                </Button>
              </div>
            ) : null
          }
        >
          {selectedIDs.length ? (
            <div className="mb-4 rounded-lg border border-dashed border-primary/30 bg-[color-mix(in_oklab,var(--primary)_8%,var(--card))] px-4 py-3 text-sm leading-6">
              已选 <span className="font-semibold text-foreground">{selectedIDs.length}</span> 条 · 本地 {selectedLocalCount} · 涉及 Notion {selectedRemoteCount}
              {hasRunningSelection ? '；运行中的会话不能删除。' : '；删除会同步删除 Notion 对话。'}
            </div>
          ) : null}

          {filteredConversations.length ? (
            <ScrollArea className="console-list-scroll pretty-scroll pr-3">
              <div className="space-y-3 pb-2">
                {filteredConversations.map((item) => {
                  const originLabel = conversationOriginLabel(item.origin);
                  const isChecked = selectedIDs.includes(item.id);
                  const isActive = item.id === selectedConversationId;
                  return (
                    <div
                      key={item.id}
                      className={cn(
                        'rounded-lg border px-4 py-4 transition-all',
                        isActive
                          ? 'border-primary/40 bg-[color-mix(in_oklab,var(--primary)_10%,var(--card))] shadow-soft'
                          : 'border-border/70 bg-card hover:border-primary/20 hover:bg-muted/40',
                      )}
                    >
                      <div className="flex items-start gap-3">
                        <input
                          type="checkbox"
                          className="mt-1 size-4 rounded border border-border bg-background"
                          checked={isChecked}
                          onChange={() => toggleConversation(item.id)}
                        />

                        <button type="button" className="min-w-0 flex-1 text-left" onClick={() => void onSelect(item.id)}>
                          <div className="flex items-start justify-between gap-3">
                            <div className="min-w-0 space-y-2">
                              <div className="line-clamp-2 text-sm font-semibold leading-6">{item.title || 'Untitled conversation'}</div>
                              <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                                <span className="rounded-full border px-2 py-0.5">{originLabel}</span>
                                {item.account_email ? <span className="rounded-full border px-2 py-0.5">{item.account_email}</span> : null}
                                {item.created_by_display_name ? <span>{item.created_by_display_name}</span> : null}
                              </div>
                            </div>
                            <StatusPill status={item.status} />
                          </div>

                          <div className={cn('mt-2 text-xs', isActive ? 'text-foreground/70' : 'text-muted-foreground')}>
                            {[item.source || '-', item.transport || '-', item.model || '-'].join(' · ')}
                          </div>
                          <div className={cn('mt-3 line-clamp-3 text-sm leading-6', isActive ? 'text-foreground/90' : 'text-foreground/80')}>
                            {item.preview || '暂无预览'}
                          </div>
                          <div className="mt-3 text-xs text-muted-foreground">更新于 {formatMaybeDate(item.updated_at || item.created_at)}</div>
                        </button>
                      </div>
                    </div>
                  );
                })}
              </div>
            </ScrollArea>
          ) : (
            <EmptyHint title="没有匹配会话" description={conversations.length ? '当前筛选下没有结果。' : '发送消息后会自动同步最近线程。'} />
          )}
        </InfoCard>

        {activeConversation ? (
          <div className="min-w-0 space-y-6">
            <InfoCard
              title={activeConversation.title || 'Untitled conversation'}
              description={activeConversation.preview || activeConversation.request_prompt || '暂无预览'}
              actions={
                <Button
                  variant="outline"
                  className="text-destructive hover:text-destructive"
                  disabled={busy || activeConversation.status === 'running'}
                  onClick={() => void handleSingleDelete(activeConversation.id)}
                >
                  <Trash2 className="size-4" />
                  删除并同步
                </Button>
              }
            >
              <KeyValueGrid
                items={[
                  { label: '状态', value: activeConversation.status },
                  { label: '来源', value: [conversationOriginLabel(activeConversation.origin), activeConversation.source || '-', activeConversation.transport || '-'].join(' · ') },
                  { label: '创建者', value: activeConversation.created_by_display_name || '-' },
                  { label: '模型', value: activeConversation.model || activeConversation.notion_model },
                  { label: '账号', value: activeConversation.account_email },
                  { label: 'Thread', value: activeConversation.thread_id },
                  { label: 'Trace', value: activeConversation.trace_id },
                  { label: 'Response / Completion', value: activeConversation.response_id || activeConversation.completion_id },
                  { label: '创建时间', value: formatMaybeDate(activeConversation.created_at) },
                  { label: '更新时间', value: formatMaybeDate(activeConversation.updated_at) },
                ]}
              />

              <div className="mt-5 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                <MetaTile label="当前会话" scrollable value={activeConversation.id} />
                <MetaTile label="会话来源" value={conversationOriginLabel(activeConversation.origin)} />
                <MetaTile label="消息条数" value={String(conversationMessages.length)} />
              </div>
            </InfoCard>

            <InfoCard
              title="消息时间线"
              description={activeConversation.status === 'running' ? '实时流进行中' : activeConversation.status === 'failed' ? activeConversation.error || '会话失败' : '已完成'}
            >
              {conversationMessages.length ? (
                <ScrollArea className="console-detail-scroll pretty-scroll pr-4">
                  <div className="space-y-4 pb-2">
                    {conversationMessages.map((message, index) => {
                      const isUser = message.role === 'user';
                      return (
                        <div
                          key={message.id || `${message.role}-${index}`}
                          className={cn(
                            'rounded-xl border p-4 shadow-soft',
                            isUser
                              ? 'border-primary/25 bg-[color-mix(in_oklab,var(--primary)_8%,var(--card))]'
                              : 'border-border/70 bg-card',
                          )}
                        >
                          <div className="mb-3 flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
                            <div className="section-eyebrow">{message.role || 'assistant'}</div>
                            <div className="text-xs text-muted-foreground">
                              {message.status || '-'} · {formatMaybeDate(message.updated_at || message.created_at)}
                            </div>
                          </div>
                          <div className="rounded-lg border bg-background px-4 py-3 text-sm leading-7 whitespace-pre-wrap break-words">
                            {message.content || '[无文本内容]'}
                          </div>
                          {message.attachments?.length ? (
                            <div className="mt-3">
                              <FileChips items={message.attachments} />
                            </div>
                          ) : null}
                        </div>
                      );
                    })}
                  </div>
                </ScrollArea>
              ) : (
                <EmptyHint title="暂无消息" description="该会话详情还没被加载，或目前只有摘要信息。" />
              )}
            </InfoCard>
          </div>
        ) : (
          <EmptyHint title="请选择一条会话" description="选中后查看元数据与消息时间线。" />
        )}
      </div>
    </div>
  );
}
