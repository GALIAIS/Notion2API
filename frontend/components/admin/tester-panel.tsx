'use client';

import { useMemo, useState } from 'react';
import { toast } from 'sonner';
import { Copy, FileImage, Search, SendHorizonal, Sparkles, type LucideIcon } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';
import { InfoCard, JsonPreview, MetaTile, PanelHeader, StatCard } from '@/components/admin/shared';
import { copyText, readFilesAsAttachments } from '@/lib/services/core/api-client';
import type { ModelItem } from '@/lib/services/admin/types';

const SELECT_TRIGGER_CLASS = 'h-10 w-full rounded-lg border-input bg-transparent';

function buildTesterConversationID(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return `conv_${crypto.randomUUID().replace(/-/g, '')}`;
  }
  return `conv_${Date.now().toString(36)}${Math.random().toString(36).slice(2, 10)}`;
}

function ToggleTile({
  icon: Icon,
  label,
  description,
  checked,
  onCheckedChange,
}: {
  icon: LucideIcon;
  label: string;
  description: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <div className="surface-subtle min-w-0 px-4 py-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 space-y-1">
          <div className="flex items-center gap-2 text-sm font-semibold tracking-tight">
            <Icon className="size-4 text-primary" />
            {label}
          </div>
          <p className="text-[13px] leading-6 text-muted-foreground">{description}</p>
        </div>
        <Switch checked={checked} onCheckedChange={onCheckedChange} />
      </div>
    </div>
  );
}

export function TesterPanel({
  models,
  defaultModel,
  defaultWebSearch,
  onRun,
}: {
  models: ModelItem[];
  defaultModel?: string;
  defaultWebSearch: boolean;
  onRun: (payload: {
    prompt: string;
    model: string;
    use_web_search: boolean;
    attachments: Awaited<ReturnType<typeof readFilesAsAttachments>>;
    conversation_id?: string;
  }) => Promise<unknown>;
}) {
  const [prompt, setPrompt] = useState('');
  const [model, setModel] = useState(defaultModel || models[0]?.id || 'auto');
  const [useWebSearch, setUseWebSearch] = useState(defaultWebSearch);
  const [useConversationID, setUseConversationID] = useState(false);
  const [conversationID, setConversationID] = useState('');
  const [files, setFiles] = useState<File[]>([]);
  const [output, setOutput] = useState('等待运行...');
  const [running, setRunning] = useState(false);

  const fileLabels = useMemo(() => files.map((file) => file.name), [files]);
  const promptLength = useMemo(() => prompt.trim().length, [prompt]);
  const normalizedConversationID = useMemo(() => conversationID.trim(), [conversationID]);

  const summaryCards = [
    { label: '当前模型', value: model || '-', hint: '本次测试目标模型' },
    { label: '联网开关', value: useWebSearch ? '开启' : '关闭', hint: defaultWebSearch ? '服务端默认开启' : '服务端默认关闭' },
    { label: '续聊模式', value: useConversationID ? '开启' : '关闭', hint: useConversationID ? (normalizedConversationID || '将自动生成并记住会话 ID') : '关闭后每次都是新测试' },
    { label: '附件数量', value: String(files.length), hint: fileLabels[0] || '尚未挂载附件' },
    { label: 'Prompt 长度', value: String(promptLength), hint: promptLength ? '已输入提示词' : '可只传附件测试' },
  ];

  async function performRun() {
    setRunning(true);
    setOutput('运行中...');
    try {
      const attachments = files.length ? await readFilesAsAttachments(files) : [];
      let nextConversationID = '';
      if (useConversationID) {
        nextConversationID = normalizedConversationID || buildTesterConversationID();
        if (nextConversationID !== normalizedConversationID) {
          setConversationID(nextConversationID);
        }
      }
      const payload = await onRun({
        prompt,
        model,
        use_web_search: useWebSearch,
        attachments,
        conversation_id: nextConversationID || undefined,
      });
      if (payload && typeof payload === 'object' && payload !== null) {
        const returnedConversationID = typeof (payload as { conversation_id?: unknown }).conversation_id === 'string'
          ? String((payload as { conversation_id?: string }).conversation_id).trim()
          : '';
        if (returnedConversationID) {
          setConversationID(returnedConversationID);
        }
      }
      setOutput(JSON.stringify(payload, null, 2));
      toast.success('测试完成');
    } catch (error) {
      const message = error instanceof Error ? error.message : '测试失败';
      setOutput(message);
      toast.error(message);
    } finally {
      setRunning(false);
    }
  }

  return (
    <div className="space-y-6">
      <PanelHeader
        eyebrow="API Tester"
        title="直接试跑 Nation AI"
        description="直接回归模型、附件与原始输出。"
      />

      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-5">
        {summaryCards.map((item) => (
          <StatCard key={item.label} label={item.label} value={item.value} hint={item.hint} />
        ))}
      </div>

      <div className="grid gap-6 2xl:grid-cols-[minmax(0,1.06fr)_360px]">
        <InfoCard
          title="测试请求"
          description="填写 prompt、选择模型与附件后执行。"
        >
          <div className="space-y-6">
            <div className="grid gap-2">
              <Label htmlFor="tester-prompt" className="text-sm font-semibold tracking-tight">Prompt</Label>
              <Textarea
                id="tester-prompt"
                value={prompt}
                onChange={(event) => setPrompt(event.target.value)}
                placeholder="输入测试提示词，或留空仅回归附件链路"
                className="min-h-[236px] rounded-lg bg-transparent leading-7"
              />
            </div>

            <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(280px,0.92fr)]">
              <div className="grid gap-2">
                <Label className="text-sm font-semibold tracking-tight">Model</Label>
                <Select value={model} onValueChange={setModel}>
                  <SelectTrigger className={SELECT_TRIGGER_CLASS}>
                    <SelectValue placeholder="选择模型" />
                  </SelectTrigger>
                  <SelectContent>
                    {models.map((item) => (
                      <SelectItem key={item.id} value={item.id}>
                        {item.name || item.id}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              <ToggleTile
                icon={Search}
                label="联网搜索"
                description="默认沿用服务端设置，可单次覆盖。"
                checked={useWebSearch}
                onCheckedChange={setUseWebSearch}
              />
            </div>

            <div className="grid gap-4 lg:grid-cols-[minmax(240px,0.72fr)_minmax(0,1.28fr)]">
              <ToggleTile
                icon={Sparkles}
                label="携带 conversation_id"
                description="开启后优先复用同一条测试会话；关闭则每次新建。"
                checked={useConversationID}
                onCheckedChange={setUseConversationID}
              />

              <div className="grid gap-2">
                <Label htmlFor="tester-conversation-id" className="text-sm font-semibold tracking-tight">conversation_id</Label>
                <div className="flex flex-wrap gap-2">
                  <Input
                    id="tester-conversation-id"
                    value={conversationID}
                    disabled={!useConversationID}
                    onChange={(event) => setConversationID(event.target.value)}
                    placeholder="开启后可手动输入；留空则首次运行时自动生成"
                    className="min-w-[280px] flex-1 rounded-lg bg-transparent"
                  />
                  <Button
                    type="button"
                    variant="outline"
                    disabled={!conversationID}
                    onClick={() => setConversationID('')}
                  >
                    清空
                  </Button>
                </div>
                <p className="text-xs leading-5 text-muted-foreground">
                  运行成功后会自动回填最新的 <code className="rounded bg-muted px-1">conversation_id</code>。
                </p>
              </div>
            </div>

            <div className="grid gap-3">
              <Label htmlFor="tester-files" className="text-sm font-semibold tracking-tight">附件</Label>
              <Input
                id="tester-files"
                type="file"
                multiple
                accept="application/pdf,text/csv,image/png,image/jpeg,image/gif,image/webp,image/heic"
                className="h-auto rounded-lg bg-transparent py-3"
                onChange={(event) => setFiles(Array.from(event.target.files || []))}
              />
              <p className="text-xs leading-5 text-muted-foreground">支持图片、PDF、CSV；浏览器会转成 data URL 后提交到 <code className="rounded bg-muted px-1">/admin/test</code>。</p>
              <div className="surface-subtle min-h-[60px] rounded-lg p-3">
                {fileLabels.length ? (
                  <div className="flex flex-wrap gap-2">
                    {fileLabels.map((label) => (
                      <div key={label} className="inline-flex items-center gap-2 rounded-lg border border-primary/20 bg-[color-mix(in_oklab,var(--primary)_12%,var(--card))] px-3 py-1.5 text-sm font-medium text-primary">
                        <FileImage className="size-4" />
                        {label}
                      </div>
                    ))}
                  </div>
                ) : (
                  <div className="flex h-full items-center text-sm text-muted-foreground">当前未选择附件。</div>
                )}
              </div>
            </div>

            <div className="flex flex-wrap gap-3">
              <Button
                className="px-4"
                disabled={running || (!prompt.trim() && files.length === 0)}
                onClick={() => void performRun()}
              >
                <SendHorizonal className="size-4" />
                {running ? '运行中...' : '运行测试'}
              </Button>
              <Button
                variant="outline"
                onClick={async () => {
                  try {
                    await copyText(output);
                    toast.success('结果已复制');
                  } catch (error) {
                    toast.error(error instanceof Error ? error.message : '复制失败');
                  }
                }}
              >
                <Copy className="size-4" />
                复制结果
              </Button>
            </div>
          </div>
        </InfoCard>

        <aside className="pretty-scroll min-w-0 space-y-5 self-start xl:sticky xl:top-6 xl:max-h-[calc(100vh-3rem)] xl:overflow-y-auto xl:pr-1">
          <InfoCard
            title="本次执行摘要"
            description="本次请求参数一览。"
          >
            <div className="grid gap-3">
              <MetaTile label="模型" scrollable value={model || '-'} />
              <MetaTile label="联网" value={useWebSearch ? '开启' : '关闭'} />
              <MetaTile
                label="conversation_id"
                scrollable
                value={useConversationID ? (normalizedConversationID || '运行时自动生成') : '未携带'}
              />
              <MetaTile
                label="附件"
                scrollable
                value={fileLabels.length ? fileLabels.join(' · ') : '未挂载附件'}
              />
              <MetaTile label="输出格式" value="Raw JSON" />
            </div>
            <div className="mt-4 rounded-xl border border-dashed bg-muted/30 px-4 py-3 text-sm leading-6 text-muted-foreground">
              <div className="mb-2 flex items-center gap-2 font-semibold text-foreground">
                <Sparkles className="size-4 text-primary" />
                测试建议
              </div>
              先用短 prompt 验证账号与模型，再追加图片、PDF、CSV 回归附件链路。
            </div>
          </InfoCard>

          <JsonPreview title="输出" value={output} minHeight={320} />
        </aside>
      </div>
    </div>
  );
}
