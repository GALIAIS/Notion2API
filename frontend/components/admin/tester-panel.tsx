'use client';

import { useCallback, useEffect, useMemo, useState } from 'react';
import { toast } from 'sonner';
import {
  Activity,
  Copy,
  FileImage,
  Plug,
  RefreshCw,
  Search,
  SendHorizonal,
  Sparkles,
  Stethoscope,
  type LucideIcon,
} from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';
import { EmptyHint, InfoCard, JsonPreview, MetaTile, PanelHeader, StatCard, Subsection } from '@/components/admin/shared';
import { copyText, readFilesAsAttachments } from '@/lib/services/core/api-client';
import type {
  DiagnosticCheck,
  DiagnosticStatus,
  DiagnosticsPayload,
  MCPPayload,
  ModelItem,
} from '@/lib/services/admin/types';

const SELECT_TRIGGER_CLASS = 'h-10 w-full rounded-lg border-input bg-transparent';

// Mirrors the check names accepted by POST /admin/diagnostics. An empty
// selection means "run everything", so the UI always sends an explicit list.
const DIAGNOSTIC_CHECKS: Array<{ name: string; label: string; description: string }> = [
  { name: 'account_pool', label: '账号池调度', description: '统计可派发账号、阻塞原因与剩余并发，不请求上游。' },
  { name: 'mcp', label: 'MCP 服务器', description: '检查已配置的 MCP 进程是否存活并能列出工具。' },
  { name: 'chat', label: '对话可用性', description: '发送一次探针 prompt，验证账号派发与上游推理。' },
  { name: 'tool_call', label: '工具调用可用性', description: '按当前规划模式诱导一次工具调用并解析结果。' },
];

const DIAGNOSTIC_STATUS_META: Record<DiagnosticStatus, { label: string; variant: 'success' | 'destructive' | 'soft' | 'secondary' }> = {
  pass: { label: '通过', variant: 'success' },
  warn: { label: '警告', variant: 'soft' },
  fail: { label: '失败', variant: 'destructive' },
  skipped: { label: '跳过', variant: 'secondary' },
};

function DiagnosticStatusBadge({ status }: { status?: string }) {
  const meta = DIAGNOSTIC_STATUS_META[(status || '') as DiagnosticStatus];
  if (!meta) {
    return <Badge variant="secondary" className="normal-case">{status || 'unknown'}</Badge>;
  }
  return <Badge variant={meta.variant} className="normal-case">{meta.label}</Badge>;
}

function DiagnosticRow({ check }: { check: DiagnosticCheck }) {
  const [expanded, setExpanded] = useState(false);
  const hasData = Boolean(check.data && Object.keys(check.data).length);
  return (
    <div className="surface-subtle min-w-0 px-4 py-3.5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2.5">
          <DiagnosticStatusBadge status={check.status} />
          <span className="truncate text-sm font-semibold tracking-tight">{check.label || check.name}</span>
          <code className="rounded bg-muted px-1 text-[11px] text-muted-foreground">{check.name}</code>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-xs text-muted-foreground">{check.duration_ms} ms</span>
          {hasData ? (
            <Button variant="ghost" size="sm" onClick={() => setExpanded((value) => !value)}>
              {expanded ? '收起' : '详情'}
            </Button>
          ) : null}
        </div>
      </div>
      {check.detail ? (
        <p className="mt-2 break-all text-[13px] leading-6 text-muted-foreground">{check.detail}</p>
      ) : null}
      {expanded && hasData ? (
        <pre className="code-surface pretty-scroll mt-3 max-h-72 overflow-auto whitespace-pre-wrap border px-3 py-2 font-mono text-[12px] leading-6">
          {JSON.stringify(check.data, null, 2)}
        </pre>
      ) : null}
    </div>
  );
}

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
  onRunDiagnostics,
  onLoadMCP,
  onReloadMCP,
  onCallMCPTool,
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
  onRunDiagnostics: (payload: { model?: string; checks?: string[] }) => Promise<DiagnosticsPayload>;
  onLoadMCP: () => Promise<MCPPayload>;
  onReloadMCP: () => Promise<MCPPayload>;
  onCallMCPTool: (payload: { name: string; arguments?: Record<string, unknown> }) => Promise<unknown>;
}) {
  const [prompt, setPrompt] = useState('');
  const [model, setModel] = useState(defaultModel || models[0]?.id || 'auto');
  const [useWebSearch, setUseWebSearch] = useState(defaultWebSearch);
  const [useConversationID, setUseConversationID] = useState(false);
  const [conversationID, setConversationID] = useState('');
  const [files, setFiles] = useState<File[]>([]);
  const [output, setOutput] = useState('等待运行...');
  const [running, setRunning] = useState(false);

  const [selectedChecks, setSelectedChecks] = useState<string[]>(() => DIAGNOSTIC_CHECKS.map((item) => item.name));
  const [diagnostics, setDiagnostics] = useState<DiagnosticsPayload | null>(null);
  const [diagnosing, setDiagnosing] = useState(false);

  const [mcp, setMCP] = useState<MCPPayload | null>(null);
  const [mcpLoading, setMCPLoading] = useState(false);
  const [mcpTool, setMCPTool] = useState('');
  const [mcpArguments, setMCPArguments] = useState('{}');
  const [mcpOutput, setMCPOutput] = useState('尚未调用工具。');
  const [mcpCalling, setMCPCalling] = useState(false);

  const fileLabels = useMemo(() => files.map((file) => file.name), [files]);
  const promptLength = useMemo(() => prompt.trim().length, [prompt]);
  const normalizedConversationID = useMemo(() => conversationID.trim(), [conversationID]);

  const diagnosticChecks = diagnostics?.checks ?? [];
  const diagnosticSummary = diagnostics?.summary;
  const mcpTools = mcp?.tools ?? [];

  // The backend only tracks runtime state for servers it actually launched, so a
  // configured-but-disabled entry never shows up in `servers`. Merge the two
  // lists so those still render as 已禁用 instead of vanishing from the console.
  const mcpServers = useMemo(() => {
    const runtime = mcp?.servers ?? [];
    const configured = mcp?.configured ?? [];
    const running = new Map(runtime.map((server) => [server.name, server]));
    const merged = [...runtime];
    configured.forEach((server) => {
      if (!running.has(server.name)) merged.push(server);
    });
    return merged;
  }, [mcp?.configured, mcp?.servers]);

  function toggleCheck(name: string, checked: boolean) {
    setSelectedChecks((current) => {
      if (checked) {
        return current.includes(name) ? current : [...current, name];
      }
      return current.filter((item) => item !== name);
    });
  }

  async function performDiagnostics() {
    if (!selectedChecks.length) {
      toast.error('请至少选择一项自检');
      return;
    }
    setDiagnosing(true);
    try {
      const payload = await onRunDiagnostics({ model, checks: selectedChecks });
      setDiagnostics(payload);
      const failed = payload.summary?.failed ?? 0;
      const warned = payload.summary?.warned ?? 0;
      if (failed > 0) {
        toast.error(`自检完成，${failed} 项失败`);
      } else if (warned > 0) {
        toast.warning(`自检完成，${warned} 项警告`);
      } else {
        toast.success('自检全部通过');
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : '自检失败';
      setDiagnostics(null);
      toast.error(message);
    } finally {
      setDiagnosing(false);
    }
  }

  const loadMCP = useCallback(
    async (reload: boolean) => {
      setMCPLoading(true);
      try {
        const payload = reload ? await onReloadMCP() : await onLoadMCP();
        setMCP(payload);
        if (reload) {
          toast.success('MCP 服务器已重载');
        }
      } catch (error) {
        toast.error(error instanceof Error ? error.message : '读取 MCP 状态失败');
      } finally {
        setMCPLoading(false);
      }
    },
    [onLoadMCP, onReloadMCP],
  );

  // Pull MCP status once on mount so the section is not empty before the
  // operator interacts with it. A missing/disabled host simply yields no servers.
  useEffect(() => {
    void loadMCP(false);
  }, [loadMCP]);

  async function performMCPCall() {
    const name = mcpTool.trim();
    if (!name) {
      toast.error('请选择要调用的 MCP 工具');
      return;
    }
    let parsed: Record<string, unknown> = {};
    const rawArguments = mcpArguments.trim();
    if (rawArguments) {
      try {
        const value: unknown = JSON.parse(rawArguments);
        if (!value || typeof value !== 'object' || Array.isArray(value)) {
          throw new Error('参数必须是 JSON 对象');
        }
        parsed = value as Record<string, unknown>;
      } catch (error) {
        toast.error(error instanceof Error ? error.message : '参数不是合法 JSON');
        return;
      }
    }
    setMCPCalling(true);
    setMCPOutput('调用中...');
    try {
      const payload = await onCallMCPTool({ name, arguments: parsed });
      setMCPOutput(JSON.stringify(payload, null, 2));
      toast.success('工具调用完成');
    } catch (error) {
      const message = error instanceof Error ? error.message : '工具调用失败';
      setMCPOutput(message);
      toast.error(message);
    } finally {
      setMCPCalling(false);
    }
  }

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

      <InfoCard
        title="可用性自检"
        description="逐项检查账号池、MCP、对话与工具调用链路；每项独立汇报，单项失败不会掩盖其他结果。"
        actions={
          <Button disabled={diagnosing} onClick={() => void performDiagnostics()}>
            <Stethoscope className="size-4" />
            {diagnosing ? '自检中...' : '开始自检'}
          </Button>
        }
      >
        <div className="space-y-5">
          <Subsection
            eyebrow="Checks"
            title="选择检查项"
            description="对话与工具调用会真实请求上游并消耗账号额度；账号池与 MCP 仅读取本地状态。"
            icon={Activity}
          >
            <div className="grid gap-3 md:grid-cols-2">
              {DIAGNOSTIC_CHECKS.map((item) => (
                <div key={item.name} className="surface-subtle min-w-0 px-4 py-3.5">
                  <div className="flex items-start justify-between gap-4">
                    <div className="min-w-0 space-y-1">
                      <div className="text-sm font-semibold tracking-tight">{item.label}</div>
                      <p className="text-[13px] leading-6 text-muted-foreground">{item.description}</p>
                    </div>
                    <Switch
                      checked={selectedChecks.includes(item.name)}
                      onCheckedChange={(checked) => toggleCheck(item.name, checked)}
                    />
                  </div>
                </div>
              ))}
            </div>
          </Subsection>

          {diagnosticSummary ? (
            <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
              <StatCard label="检查项" value={String(diagnosticSummary.total ?? diagnosticChecks.length)} hint={diagnostics?.model ? `模型 ${diagnostics.model}` : undefined} />
              <StatCard label="通过" value={String(diagnosticSummary.passed ?? 0)} hint="含跳过项" />
              <StatCard label="警告" value={String(diagnosticSummary.warned ?? 0)} hint="链路可用但行为不理想" />
              <StatCard label="失败" value={String(diagnosticSummary.failed ?? 0)} hint={diagnostics?.account ? `账号 ${diagnostics.account}` : '未指定账号'} />
            </div>
          ) : null}

          {diagnosticChecks.length ? (
            <div className="grid gap-3">
              {diagnosticChecks.map((check) => (
                <DiagnosticRow key={check.name} check={check} />
              ))}
            </div>
          ) : (
            <EmptyHint title="尚未自检" description="选择检查项后点击开始自检，结果会逐项列在这里。" />
          )}
        </div>
      </InfoCard>

      <InfoCard
        title="MCP 工具"
        description="查看内置 MCP 宿主的进程状态与聚合工具清单，并可直接调用工具做验证。"
        actions={
          <>
            <Button variant="outline" disabled={mcpLoading} onClick={() => void loadMCP(false)}>
              <RefreshCw className="size-4" />
              刷新
            </Button>
            <Button variant="outline" disabled={mcpLoading} onClick={() => void loadMCP(true)}>
              <Plug className="size-4" />
              重载服务器
            </Button>
          </>
        }
      >
        <div className="space-y-5">
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
            <MetaTile label="工具调用开关" value={mcp?.tools_enabled === false ? '已关闭' : '已开启'} />
            <MetaTile label="规划模式" value={mcp?.planning_mode || '—'} />
            <MetaTile label="服务器数" value={String(mcpServers.length)} />
            <MetaTile label="工具总数" value={String(mcpTools.length)} />
          </div>

          {mcpServers.length ? (
            <div className="grid gap-3">
              {mcpServers.map((server) => (
                <div key={server.name} className="surface-subtle min-w-0 px-4 py-3.5">
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <div className="flex min-w-0 items-center gap-2.5">
                      <Badge variant={server.alive ? 'success' : server.enabled === false ? 'secondary' : 'destructive'} className="normal-case">
                        {server.alive ? '运行中' : server.enabled === false ? '已禁用' : '未运行'}
                      </Badge>
                      <span className="truncate text-sm font-semibold tracking-tight">{server.name}</span>
                      <span className="text-xs text-muted-foreground">{server.tool_count ?? 0} 个工具</span>
                    </div>
                    <span className="text-xs text-muted-foreground">重启 {server.restart_count ?? 0} 次</span>
                  </div>
                  {server.command ? (
                    <code className="mt-2 block break-all text-[12px] text-muted-foreground">
                      {[server.command, ...(server.args ?? [])].join(' ')}
                    </code>
                  ) : null}
                  {server.last_error ? (
                    <p className="mt-2 break-all text-[13px] leading-6 text-destructive">{server.last_error}</p>
                  ) : null}
                </div>
              ))}
            </div>
          ) : (
            <EmptyHint
              title="未配置 MCP 服务器"
              description="在设置页的 mcp_servers 中添加条目并设为 enabled 后，网关会自行拉起进程并聚合工具。"
            />
          )}

          {mcpTools.length ? (
            <Subsection eyebrow="Invoke" title="直接调用工具" description="工具名为 server.tool 形式；参数需为 JSON 对象。注意真实工具可能产生副作用。" icon={Plug}>
              <div className="grid gap-4">
                <div className="grid gap-4 lg:grid-cols-2">
                  <div className="grid gap-2">
                    <Label className="text-sm font-semibold tracking-tight">工具</Label>
                    <Select value={mcpTool} onValueChange={setMCPTool}>
                      <SelectTrigger className={SELECT_TRIGGER_CLASS}>
                        <SelectValue placeholder="选择工具" />
                      </SelectTrigger>
                      <SelectContent>
                        {mcpTools.map((tool) => (
                          <SelectItem key={tool.name} value={tool.name}>
                            {tool.name}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <p className="text-xs leading-5 text-muted-foreground">
                      {mcpTools.find((tool) => tool.name === mcpTool)?.description || '选择工具后显示描述。'}
                    </p>
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="mcp-arguments" className="text-sm font-semibold tracking-tight">参数（JSON）</Label>
                    <Textarea
                      id="mcp-arguments"
                      value={mcpArguments}
                      onChange={(event) => setMCPArguments(event.target.value)}
                      placeholder='{"key": "value"}'
                      className="min-h-[104px] rounded-lg bg-transparent font-mono text-[12px] leading-6"
                    />
                  </div>
                </div>
                <div>
                  <Button disabled={mcpCalling || !mcpTool} onClick={() => void performMCPCall()}>
                    <SendHorizonal className="size-4" />
                    {mcpCalling ? '调用中...' : '调用工具'}
                  </Button>
                </div>
                <pre className="code-surface pretty-scroll max-h-80 overflow-auto whitespace-pre-wrap border px-4 py-3 font-mono text-[12px] leading-6">
                  {mcpOutput}
                </pre>
              </div>
            </Subsection>
          ) : null}
        </div>
      </InfoCard>
    </div>
  );
}
