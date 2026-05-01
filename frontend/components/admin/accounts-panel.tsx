'use client';

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { toast } from 'sonner';
import {
  CheckCircle2,
  ChevronRight,
  MailPlus,
  RefreshCcw,
  Rocket,
  ShieldCheck,
  Trash2,
  WandSparkles,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';
import {
  EmptyHint,
  InfoCard,
  JsonPreview,
  KeyValueGrid,
  MetaTile,
  PanelHeader,
  StatCard,
  StatusPill,
  Subsection,
  formatMaybeDate,
} from '@/components/admin/shared';
import type { AccountItem, AccountsPayload, JsonResult, ModelItem } from '@/lib/services/admin/types';

interface AccountEditState {
  priority: number;
  hourlyQuota: number;
  maxConcurrency: number;
  disabled: boolean;
}

interface ManualImportState {
  email: string;
  userId: string;
  userName: string;
  spaceId: string;
  spaceName: string;
  clientVersion: string;
  cookieHeader: string;
  probeJsonText: string;
  active: boolean;
}

interface ProbeDraft {
  email?: string;
  user_id?: string;
  space_id?: string;
  client_version?: string;
}

const FIELD_CLASS = 'h-10 rounded-lg bg-transparent';
const TEXTAREA_CLASS = 'rounded-lg bg-transparent';
const PROBE_TEXTAREA_CLASS =
  'pretty-scroll h-[360px] min-h-[360px] resize-none !rounded-none !border-0 !bg-transparent px-4 py-3 font-mono text-[12px] leading-6 !shadow-none focus-visible:!ring-0 lg:h-[440px] lg:min-h-[440px]';

const defaultManualImportState: ManualImportState = {
  email: '',
  userId: '',
  userName: '',
  spaceId: '',
  spaceName: '',
  clientVersion: '',
  cookieHeader: '',
  probeJsonText: '',
  active: true,
};

function buildAccountEditMap(items: AccountItem[]): Record<string, AccountEditState> {
  return items.reduce<Record<string, AccountEditState>>((accumulator, item) => {
    if (!item.email) return accumulator;
    accumulator[item.email] = {
      priority: Number(item.priority ?? 0),
      hourlyQuota: Number(item.hourly_quota ?? 0),
      maxConcurrency: Math.max(1, Number(item.max_concurrency ?? 1)),
      disabled: Boolean(item.disabled),
    };
    return accumulator;
  }, {});
}

function safeParseProbeJSON(raw: string): ProbeDraft | null {
  const text = raw.trim();
  if (!text) return null;
  const parsed = JSON.parse(text) as ProbeDraft;
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error('Probe JSON 必须是对象');
  }
  return parsed;
}

function quotaText(item: AccountItem) {
  if (!item.quota_limited) return 'unlimited';
  return `${item.remaining_quota ?? 0}/${item.hourly_quota ?? 0}`;
}

function DetailField({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <div className="space-y-2">
      <div className="space-y-1">
        <Label className="text-sm font-semibold tracking-tight">{label}</Label>
        {hint ? <p className="text-xs leading-5 text-muted-foreground">{hint}</p> : null}
      </div>
      {children}
    </div>
  );
}

function AccountListItem({
  item,
  selected,
  onSelect,
}: {
  item: AccountItem & { email: string };
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className={[
        'w-full rounded-lg border px-4 py-4 text-left transition-all',
        selected
          ? 'border-primary/40 bg-[color-mix(in_oklab,var(--primary)_10%,var(--card))] shadow-soft'
          : 'border-border/70 bg-card hover:border-primary/20 hover:bg-muted/40',
      ].join(' ')}
    >
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 space-y-2">
          <div className="break-all text-sm font-semibold leading-6">{item.email}</div>
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <StatusPill status={item.status} />
            <span>{item.active ? 'active' : 'standby'}</span>
            <span>{item.disabled ? 'disabled' : 'enabled'}</span>
          </div>
        </div>
        <ChevronRight className={['mt-0.5 size-4 shrink-0', selected ? 'text-primary' : 'text-muted-foreground'].join(' ')} />
      </div>
      <div className="mt-3 grid gap-2 text-xs leading-5 text-muted-foreground sm:grid-cols-2">
        <div>quota · {quotaText(item)}</div>
        <div>prio · {item.priority ?? 0}</div>
        <div>last login · {formatMaybeDate(item.last_login_at)}</div>
        <div>space · {item.space_name || item.space_id || '-'}</div>
      </div>
    </button>
  );
}

export function AccountsPanel({
  accountsPayload,
  models,
  defaultModel,
  onRefresh,
  onStartLogin,
  onVerifyCode,
  onImportAccount,
  onQuickTest,
  onActivate,
  onDelete,
  onSaveAccountSettings,
}: {
  accountsPayload: AccountsPayload | null;
  models: ModelItem[];
  defaultModel?: string;
  onRefresh: () => Promise<unknown>;
  onStartLogin: (email: string) => Promise<unknown>;
  onVerifyCode: (email: string, code: string) => Promise<unknown>;
  onImportAccount: (payload: JsonResult) => Promise<unknown>;
  onQuickTest: (payload: JsonResult) => Promise<unknown>;
  onActivate: (email: string) => Promise<unknown>;
  onDelete: (email: string) => Promise<unknown>;
  onSaveAccountSettings: (payload: JsonResult) => Promise<unknown>;
}) {
  const items = accountsPayload?.items || [];
  const activeAccount = accountsPayload?.active_account || '';
  const loginHelper = accountsPayload?.login_helper;
  const runtimeSession = accountsPayload?.session;
  const refreshRuntime = accountsPayload?.session_refresh_runtime;

  const [startEmail, setStartEmail] = useState('');
  const [startMessage, setStartMessage] = useState('');
  const [starting, setStarting] = useState(false);

  const [verifyEmail, setVerifyEmail] = useState('');
  const [verifyCode, setVerifyCode] = useState('');
  const [verifyMessage, setVerifyMessage] = useState('');
  const [verifying, setVerifying] = useState(false);

  const [manual, setManual] = useState<ManualImportState>(defaultManualImportState);
  const [manualHint, setManualHint] = useState('');
  const [manualBusy, setManualBusy] = useState(false);

  const [quickTestEmail, setQuickTestEmail] = useState('');
  const [quickTestModel, setQuickTestModel] = useState(defaultModel || models[0]?.id || 'auto');
  const [quickTestPrompt, setQuickTestPrompt] = useState('Reply with NOTION2API_ACCOUNT_OK only.');
  const [quickTestMessage, setQuickTestMessage] = useState('');
  const [quickTestOutput, setQuickTestOutput] = useState('等待测试...');
  const [quickTesting, setQuickTesting] = useState(false);

  const [accountEdits, setAccountEdits] = useState<Record<string, AccountEditState>>({});
  const [selectedEmail, setSelectedEmail] = useState('');

  useEffect(() => {
    setAccountEdits(buildAccountEditMap(items));
    const preferredEmail = activeAccount || items[0]?.email || '';
    setQuickTestEmail((current) => (current && items.some((item) => item.email === current) ? current : preferredEmail));
    setStartEmail((current) => current || preferredEmail);
    setVerifyEmail((current) => current || preferredEmail);
    setSelectedEmail((current) => (current && items.some((item) => item.email === current) ? current : preferredEmail));
  }, [activeAccount, items]);

  useEffect(() => {
    setQuickTestModel((current) => current || defaultModel || models[0]?.id || 'auto');
  }, [defaultModel, models]);

  useEffect(() => {
    const text = manual.probeJsonText.trim();
    if (!text) {
      setManualHint('');
      return;
    }
    try {
      const payload = safeParseProbeJSON(text);
      if (!payload) return;
      setManual((current) => ({
        ...current,
        email: payload.email?.trim() || current.email,
        userId: payload.user_id?.trim() || current.userId,
        spaceId: payload.space_id?.trim() || current.spaceId,
        clientVersion: payload.client_version?.trim() || current.clientVersion,
      }));
      const extracted = [
        payload.email ? 'Email' : '',
        payload.user_id ? 'User ID' : '',
        payload.space_id ? 'Space ID' : '',
        payload.client_version ? 'Client Version' : '',
      ].filter(Boolean);
      setManualHint(extracted.length ? `已从 Probe JSON 自动提取 ${extracted.join(' / ')}` : '');
    } catch (error) {
      setManualHint(error instanceof Error ? error.message : 'Probe JSON 解析失败');
    }
  }, [manual.probeJsonText]);

  const accountOptions = useMemo(
    () => items.filter((item): item is AccountItem & { email: string } => Boolean(item.email)),
    [items],
  );

  const selectedAccount = useMemo(
    () => accountOptions.find((item) => item.email === selectedEmail) || null,
    [accountOptions, selectedEmail],
  );

  const modelOptions = useMemo(() => models.filter((item) => item.id), [models]);

  const selectedEdit = selectedAccount?.email
    ? accountEdits[selectedAccount.email] || { priority: 0, hourlyQuota: 0, maxConcurrency: 1, disabled: false }
    : { priority: 0, hourlyQuota: 0, maxConcurrency: 1, disabled: false };

  const summaryCards = [
    {
      label: '账号池',
      value: `${items.length}`,
      hint: activeAccount ? `active · ${activeAccount}` : '尚未激活默认账号',
    },
    {
      label: '会话状态',
      value: accountsPayload?.session_ready ? 'READY' : 'NOT READY',
      hint: runtimeSession?.space_name || runtimeSession?.space_id || '尚未绑定空间',
    },
    {
      label: '登录 Helper',
      value: `timeout ${loginHelper?.timeout_sec || 120}s`,
      hint: loginHelper?.sessions_dir || '使用默认 sessions_dir',
    },
    {
      label: '刷新状态',
      value: refreshRuntime?.last_error ? 'ERROR' : 'IDLE',
      hint: refreshRuntime?.last_refresh_at || refreshRuntime?.last_error || '暂无刷新记录',
    },
  ];

  const runtimeCards = [
    { label: '活跃账号', value: activeAccount || '-' },
    { label: '会话状态', value: accountsPayload?.session_ready ? 'READY' : 'NOT READY' },
    { label: 'Helper', value: `go-native · timeout ${loginHelper?.timeout_sec || 120}s` },
    { label: 'Sessions Dir', value: loginHelper?.sessions_dir || '-' },
    { label: '当前空间', value: runtimeSession?.space_name || runtimeSession?.space_id || '-' },
    { label: '最近刷新', value: refreshRuntime?.last_refresh_at || refreshRuntime?.last_error || '-' },
  ];

  function populateEmail(email: string) {
    setSelectedEmail(email);
    setStartEmail(email);
    setVerifyEmail(email);
    setQuickTestEmail(email);
    setManual((current) => ({ ...current, email }));
  }

  function updateAccountEdit(email: string, patch: Partial<AccountEditState>) {
    setAccountEdits((current) => ({
      ...current,
      [email]: {
        priority: current[email]?.priority ?? 0,
        hourlyQuota: current[email]?.hourlyQuota ?? 0,
        maxConcurrency: current[email]?.maxConcurrency ?? 1,
        disabled: current[email]?.disabled ?? false,
        ...patch,
      },
    }));
  }

  async function runQuickTest(email: string) {
    setQuickTesting(true);
    setQuickTestEmail(email);
    setQuickTestMessage('测试中...');
    setQuickTestOutput('运行中...');
    try {
      const payload = await onQuickTest({
        email,
        model: quickTestModel,
        prompt: quickTestPrompt.trim() || 'Reply with NOTION2API_ACCOUNT_OK only.',
      });
      setQuickTestOutput(JSON.stringify(payload, null, 2));
      setQuickTestMessage('测试成功');
      toast.success(`账号 ${email} 测试成功`);
    } catch (error) {
      const message = error instanceof Error ? error.message : '账号测试失败';
      setQuickTestMessage(message);
      setQuickTestOutput(message);
      toast.error(message);
    } finally {
      setQuickTesting(false);
    }
  }

  async function saveAccount(email: string, edit: AccountEditState) {
    try {
      await onSaveAccountSettings({
        email,
        priority: edit.priority,
        hourly_quota: edit.hourlyQuota,
        max_concurrency: edit.maxConcurrency,
        disabled: edit.disabled,
      });
      toast.success(`已保存 ${email}`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : '保存账号设置失败');
    }
  }

  async function activateAccount(email: string) {
    try {
      await onActivate(email);
      toast.success(`已激活 ${email}`);
      populateEmail(email);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : '激活账号失败');
    }
  }

  async function deleteAccount(email: string) {
    if (!window.confirm(`确认删除账号 ${email} ?`)) return;
    try {
      await onDelete(email);
      toast.success(`已删除 ${email}`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : '删除账号失败');
    }
  }

  async function performManualImport() {
    setManualBusy(true);
    setManualHint('导入中...');
    try {
      let probe: ProbeDraft | null = null;
      if (manual.probeJsonText.trim()) {
        probe = safeParseProbeJSON(manual.probeJsonText);
      }
      const email = manual.email.trim() || probe?.email?.trim() || '';
      if (!email && !manual.cookieHeader.trim() && !manual.probeJsonText.trim()) {
        throw new Error('请至少提供 cookie_header、Probe JSON 或邮箱');
      }
      const payload = await onImportAccount({
        email,
        user_id: manual.userId.trim(),
        user_name: manual.userName.trim(),
        space_id: manual.spaceId.trim(),
        space_name: manual.spaceName.trim(),
        client_version: manual.clientVersion.trim(),
        cookie_header: manual.cookieHeader.trim(),
        probe_json_text: manual.probeJsonText.trim(),
        active: manual.active,
      });
      const importedEmail = String(
        (payload as { account?: { email?: string }; status?: { email?: string } }).account?.email ||
          (payload as { status?: { email?: string } }).status?.email ||
          email,
      );
      populateEmail(importedEmail);
      setManual((current) => ({ ...current, email: importedEmail }));
      setManualHint(`账号 ${importedEmail} 已导入`);
      toast.success('手动导入成功');
    } catch (error) {
      const message = error instanceof Error ? error.message : '手动导入失败';
      setManualHint(message);
      toast.error(message);
    } finally {
      setManualBusy(false);
    }
  }

  return (
    <div className="space-y-6">
      <PanelHeader
        eyebrow="Accounts"
        title="账号、验证码登录与手动导入"
        description="集中处理登录、导入、调度与校验。"
        actions={
          <Button variant="outline" onClick={() => void onRefresh()}>
            <RefreshCcw className="size-4" />
            刷新账号
          </Button>
        }
      />

      <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-4">
        {summaryCards.map((item) => (
          <StatCard key={item.label} label={item.label} value={item.value} hint={item.hint} />
        ))}
      </div>

      <div className="grid gap-6 2xl:grid-cols-[minmax(0,1.04fr)_360px]">
        <div className="min-w-0 space-y-6">
          <InfoCard
            title="验证码登录管线"
            description="发起验证码请求、提交验证码，同步动作会自动填充到其他区域。"
          >
            <div className="grid gap-4 xl:grid-cols-2">
              <form
                className="min-w-0"
                onSubmit={async (event) => {
                  event.preventDefault();
                  if (!startEmail.trim()) {
                    setStartMessage('请输入邮箱');
                    return;
                  }
                  setStarting(true);
                  setStartMessage('请求验证码中...');
                  try {
                    const payload = await onStartLogin(startEmail.trim());
                    setVerifyEmail(startEmail.trim());
                    setQuickTestEmail(startEmail.trim());
                    setSelectedEmail(startEmail.trim());
                    setStartMessage(
                      String(
                        (payload as { status?: { message?: string } })?.status?.message ||
                          '验证码已发送，请在右侧输入验证码',
                      ),
                    );
                    toast.success('验证码请求成功');
                  } catch (error) {
                    const message = error instanceof Error ? error.message : '请求验证码失败';
                    setStartMessage(message);
                    toast.error(message);
                  } finally {
                    setStarting(false);
                  }
                }}
              >
                <Subsection eyebrow="Step 1" title="请求验证码" description="发起登录，同步到验证码区与测试区。" icon={MailPlus}>
                  <div className="space-y-4">
                    <DetailField label="Email" hint="建议使用你准备加入账号池的邮箱。">
                      <Input
                        id="account-start-email"
                        value={startEmail}
                        onChange={(event) => setStartEmail(event.target.value)}
                        placeholder="name@example.com"
                        className={FIELD_CLASS}
                      />
                    </DetailField>
                    <div className="flex flex-wrap items-center gap-3">
                      <Button type="submit" className="px-4" disabled={starting}>
                        <Rocket className="size-4" />
                        {starting ? '请求中...' : '请求验证码'}
                      </Button>
                      <p className="text-sm leading-6 text-muted-foreground">
                        {startMessage || '收到验证码后直接提交。'}
                      </p>
                    </div>
                  </div>
                </Subsection>
              </form>

              <form
                className="min-w-0"
                onSubmit={async (event) => {
                  event.preventDefault();
                  if (!verifyEmail.trim() || !verifyCode.trim()) {
                    setVerifyMessage('请输入邮箱和验证码');
                    return;
                  }
                  setVerifying(true);
                  setVerifyMessage('验证中...');
                  try {
                    const payload = await onVerifyCode(verifyEmail.trim(), verifyCode.trim());
                    populateEmail(verifyEmail.trim());
                    setVerifyCode('');
                    setVerifyMessage(
                      String(
                        (payload as { status?: { message?: string } })?.status?.message ||
                          '验证成功，账号已自动激活',
                      ),
                    );
                    toast.success('验证码验证成功');
                  } catch (error) {
                    const message = error instanceof Error ? error.message : '验证码验证失败';
                    setVerifyMessage(message);
                    toast.error(message);
                  } finally {
                    setVerifying(false);
                  }
                }}
              >
                <Subsection eyebrow="Step 2" title="填写验证码" description="验证成功后会自动落盘并切为默认账号。" icon={ShieldCheck}>
                  <div className="space-y-4">
                    <div className="grid gap-4 md:grid-cols-2">
                      <DetailField label="Email" hint="通常与左侧请求验证码所用邮箱保持一致。">
                        <Input
                          id="account-verify-email"
                          value={verifyEmail}
                          onChange={(event) => setVerifyEmail(event.target.value)}
                          placeholder="与左侧邮箱一致"
                          className={FIELD_CLASS}
                        />
                      </DetailField>
                      <DetailField label="Code" hint="六位验证码，提交后会立刻进入账号池。">
                        <Input
                          id="account-verify-code"
                          value={verifyCode}
                          onChange={(event) => setVerifyCode(event.target.value)}
                          placeholder="六位验证码"
                          className={[FIELD_CLASS, 'tracking-[0.32em]'].join(' ')}
                        />
                      </DetailField>
                    </div>
                    <div className="flex flex-wrap items-center gap-3">
                      <Button type="submit" className="px-4" disabled={verifying}>
                        <CheckCircle2 className="size-4" />
                        {verifying ? '验证中...' : '提交验证码'}
                      </Button>
                      <p className="text-sm leading-6 text-muted-foreground">
                        {verifyMessage || '错误时会显示服务端返回信息。'}
                      </p>
                    </div>
                  </div>
                </Subsection>
              </form>
            </div>
          </InfoCard>

          <InfoCard
            title="手动导入账号"
            description="只贴 cookie / token_v2 也能自动补齐，也可直接导入完整 Probe JSON。"
            actions={
              <div className="text-sm leading-6 text-muted-foreground">
                {manualHint || '只填 token_v2 会尝试补齐其他字段。'}
              </div>
            }
          >
            <div className="grid gap-4 xl:grid-cols-[minmax(0,0.94fr)_minmax(320px,1.06fr)]">
              <Subsection eyebrow="Identity" title="账号身份" description="6 个常用字段可以手动填写，也会从右侧 Probe JSON 自动提取。">
                <div className="space-y-5">
                  <div className="grid gap-4 md:grid-cols-2">
                    <DetailField label="Email" hint="可留空；若 cookie 或 Probe JSON 足够完整，会自动识别。">
                      <Input value={manual.email} onChange={(event) => setManual((current) => ({ ...current, email: event.target.value }))} className={FIELD_CLASS} />
                    </DetailField>
                    <DetailField label="User ID">
                      <Input value={manual.userId} onChange={(event) => setManual((current) => ({ ...current, userId: event.target.value }))} className={FIELD_CLASS} />
                    </DetailField>
                    <DetailField label="Space ID">
                      <Input value={manual.spaceId} onChange={(event) => setManual((current) => ({ ...current, spaceId: event.target.value }))} className={FIELD_CLASS} />
                    </DetailField>
                    <DetailField label="Client Version">
                      <Input value={manual.clientVersion} onChange={(event) => setManual((current) => ({ ...current, clientVersion: event.target.value }))} className={FIELD_CLASS} />
                    </DetailField>
                    <DetailField label="User Name">
                      <Input value={manual.userName} onChange={(event) => setManual((current) => ({ ...current, userName: event.target.value }))} className={FIELD_CLASS} />
                    </DetailField>
                    <DetailField label="Space Name">
                      <Input value={manual.spaceName} onChange={(event) => setManual((current) => ({ ...current, spaceName: event.target.value }))} className={FIELD_CLASS} />
                    </DetailField>
                  </div>

                  <DetailField label="Cookie Header" hint="支持完整 Cookie header，也支持只填 token_v2=...。">
                    <Textarea
                      value={manual.cookieHeader}
                      onChange={(event) => setManual((current) => ({ ...current, cookieHeader: event.target.value }))}
                      className={[TEXTAREA_CLASS, 'min-h-[120px]'].join(' ')}
                      placeholder="cookie1=value1; cookie2=value2"
                    />
                  </DetailField>

                  <div className="flex items-start justify-between gap-4 rounded-lg border bg-muted/40 px-4 py-4">
                    <div className="min-w-0 space-y-1">
                      <div className="text-sm font-semibold tracking-tight">导入后立即激活</div>
                      <p className="text-sm leading-6 text-muted-foreground">适合直接把现成会话切为当前默认账号。</p>
                    </div>
                    <Switch checked={manual.active} onCheckedChange={(checked) => setManual((current) => ({ ...current, active: checked }))} />
                  </div>

                  <Button
                    className="w-full justify-center"
                    disabled={manualBusy}
                    onClick={() => void performManualImport()}
                  >
                    <WandSparkles className="size-4" />
                    {manualBusy ? '导入中...' : '手动导入账号'}
                  </Button>
                </div>
              </Subsection>

              <Subsection eyebrow="Probe JSON" title="完整身份 JSON" description="长 JSON 统一在深色代码区编辑，可补齐上面 Identity 字段。">
                <div className="space-y-2">
                  <Label className="sr-only">Probe JSON</Label>
                  <div className="code-surface overflow-hidden rounded-xl border">
                    <Textarea
                      value={manual.probeJsonText}
                      onChange={(event) => setManual((current) => ({ ...current, probeJsonText: event.target.value }))}
                      className={PROBE_TEXTAREA_CLASS}
                      placeholder='{"email":"name@example.com","user_id":"...","space_id":"...","client_version":"...","cookies":[{"name":"token_v2","value":"..."}]}'
                    />
                  </div>
                  <p className="text-xs leading-5 text-muted-foreground">在上方表单已填的字段优先；Probe JSON 仅补齐未填字段。</p>
                </div>
              </Subsection>
            </div>
          </InfoCard>

          <div className="grid gap-6 xl:grid-cols-[minmax(320px,360px)_minmax(0,1fr)] 2xl:grid-cols-[minmax(340px,0.8fr)_minmax(0,1.2fr)]">
            <InfoCard title="账号池" description={`共 ${accountOptions.length} 个账号，点击查看详情。`}>
              {accountOptions.length ? (
                <ScrollArea className="console-list-scroll pretty-scroll pr-3">
                  <div className="space-y-3 pb-1">
                    {accountOptions.map((item) => (
                      <AccountListItem
                        key={item.email}
                        item={item}
                        selected={item.email === selectedEmail}
                        onSelect={() => {
                          setSelectedEmail(item.email);
                          setQuickTestEmail(item.email);
                        }}
                      />
                    ))}
                  </div>
                </ScrollArea>
              ) : (
                <EmptyHint title="当前还没有账号" description="可先请求验证码，或直接导入 Probe JSON。" />
              )}
            </InfoCard>

            <InfoCard title="账号详情与操作" description="当前账号的运行信息与操作入口。">
              {selectedAccount ? (
                <div className="space-y-5">
                  <KeyValueGrid
                    items={[
                      { label: 'Email', value: selectedAccount.email },
                      { label: 'Status', value: selectedAccount.status || '-' },
                      { label: 'User', value: selectedAccount.user_name || selectedAccount.user_id || '-' },
                      { label: 'Space', value: selectedAccount.space_name || selectedAccount.space_id || '-' },
                      { label: 'Plan', value: selectedAccount.plan_type || '-' },
                      { label: 'Last Login', value: formatMaybeDate(selectedAccount.last_login_at) },
                      { label: 'Client Version', value: selectedAccount.client_version || '-' },
                    ]}
                  />

                  <div className="grid gap-4 lg:grid-cols-2">
                    <Subsection eyebrow="Scheduling" title="调度与限额" description="保存后直接写回账号池。">
                      <div className="space-y-4">
                        <div className="grid gap-4 md:grid-cols-3">
                          <DetailField label="Priority">
                            <Input
                              type="number"
                              value={selectedEdit.priority}
                              onChange={(event) =>
                                updateAccountEdit(selectedAccount.email, {
                                  priority: Number(event.target.value || 0),
                                })
                              }
                              className={FIELD_CLASS}
                            />
                          </DetailField>
                          <DetailField label="Hourly Quota" hint="0 表示不限制。">
                            <Input
                              type="number"
                              min="0"
                              value={selectedEdit.hourlyQuota}
                              onChange={(event) =>
                                updateAccountEdit(selectedAccount.email, {
                                  hourlyQuota: Math.max(0, Number(event.target.value || 0)),
                                })
                              }
                              className={FIELD_CLASS}
                            />
                          </DetailField>
                          <DetailField label="Max Concurrency" hint="每账号并发槽位，最小值 1。">
                            <Input
                              type="number"
                              min="1"
                              value={selectedEdit.maxConcurrency}
                              onChange={(event) =>
                                updateAccountEdit(selectedAccount.email, {
                                  maxConcurrency: Math.max(1, Number(event.target.value || 1)),
                                })
                              }
                              className={FIELD_CLASS}
                            />
                          </DetailField>
                        </div>
                        <div className="flex items-center justify-between gap-3 rounded-xl border bg-muted/40 px-3 py-3">
                          <div>
                            <div className="text-sm font-semibold tracking-tight">Disabled</div>
                            <p className="text-xs leading-5 text-muted-foreground">禁用后仍保留账号数据，但不参与调度。</p>
                          </div>
                          <Switch
                            checked={selectedEdit.disabled}
                            onCheckedChange={(checked) => updateAccountEdit(selectedAccount.email, { disabled: checked })}
                          />
                        </div>
                      </div>
                    </Subsection>

                    <Subsection eyebrow="Runtime" title="运行态摘要" description="最近登录、使用与失败记录。">
                      <div className="grid gap-3 sm:grid-cols-2">
                        <MetaTile label="Quota" value={quotaText(selectedAccount)} />
                        <MetaTile
                          label="Cooldown"
                          value={selectedAccount.cooldown_active ? `${selectedAccount.cooldown_remaining_sec || 0}s` : 'ready'}
                        />
                        <MetaTile
                          label="Success / Fail"
                          value={`${selectedAccount.total_successes || 0} / ${selectedAccount.total_failures || 0}`}
                        />
                        <MetaTile label="Last Used" value={formatMaybeDate(selectedAccount.last_used_at)} />
                      </div>
                    </Subsection>
                  </div>

                  <div className="grid gap-3 lg:grid-cols-2">
                    <MetaTile
                      label="Login Message"
                      scrollable
                      value={selectedAccount.login_status?.message || selectedAccount.login_status?.error || '-'}
                    />
                    <MetaTile label="Last Error" scrollable value={selectedAccount.last_error || '-'} />
                  </div>

                  <div className="grid gap-3 lg:grid-cols-3">
                    <MetaTile label="Probe JSON" scrollable value={selectedAccount.probe_json || '-'} />
                    <MetaTile label="Profile Dir" scrollable value={selectedAccount.profile_dir || '-'} />
                    <MetaTile label="Storage State" scrollable value={selectedAccount.storage_state_path || '-'} />
                  </div>

                  <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
                    <Button className="w-full" onClick={() => void saveAccount(selectedAccount.email, selectedEdit)}>
                      保存设置
                    </Button>
                    <Button className="w-full" variant="outline" onClick={() => populateEmail(selectedAccount.email)}>
                      填充到表单
                    </Button>
                    <Button className="w-full" variant="outline" onClick={() => void activateAccount(selectedAccount.email)}>
                      激活账号
                    </Button>
                    <Button className="w-full" variant="outline" onClick={() => void runQuickTest(selectedAccount.email)}>
                      测试账号
                    </Button>
                    <Button
                      variant="outline"
                      className="w-full text-destructive hover:text-destructive"
                      onClick={() => void deleteAccount(selectedAccount.email)}
                    >
                      <Trash2 className="size-4" />
                      删除账号
                    </Button>
                  </div>
                </div>
              ) : (
                <EmptyHint title="请选择一个账号" description="选择后查看详情与操作。" />
              )}
            </InfoCard>
          </div>
        </div>

        <aside className="pretty-scroll min-w-0 space-y-5 self-start xl:sticky xl:top-6 xl:max-h-[calc(100vh-3rem)] xl:overflow-y-auto xl:pr-1">
          <InfoCard title="Runtime 概览" description="账号池与会话摘要。">
            <div className="grid gap-3">
              {runtimeCards.map((item) => (
                <MetaTile key={item.label} label={item.label} scrollable value={item.value} />
              ))}
            </div>
          </InfoCard>

          <InfoCard title="快速测试指定账号" description="验证账号与模型是否可用。">
            <div className="space-y-4">
              <DetailField label="Account">
                <Select value={quickTestEmail} onValueChange={setQuickTestEmail} disabled={!accountOptions.length}>
                  <SelectTrigger className={FIELD_CLASS}>
                    <SelectValue placeholder="选择账号" />
                  </SelectTrigger>
                  <SelectContent>
                    {accountOptions.map((item) => (
                      <SelectItem key={item.email} value={item.email}>
                        {item.email}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </DetailField>
              <DetailField label="Model">
                <Select value={quickTestModel} onValueChange={setQuickTestModel}>
                  <SelectTrigger className={FIELD_CLASS}>
                    <SelectValue placeholder="选择模型" />
                  </SelectTrigger>
                  <SelectContent>
                    {modelOptions.map((item) => (
                      <SelectItem key={item.id} value={item.id}>
                        {item.name || item.id}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </DetailField>
              <DetailField label="Prompt" hint="建议先用短 prompt 探测 READY，再换长内容回归。">
                <Textarea value={quickTestPrompt} onChange={(event) => setQuickTestPrompt(event.target.value)} className={[TEXTAREA_CLASS, 'min-h-[130px]'].join(' ')} />
              </DetailField>
              <Button className="w-full justify-center" disabled={quickTesting || !quickTestEmail} onClick={() => void runQuickTest(quickTestEmail)}>
                <Rocket className="size-4" />
                {quickTesting ? '测试中...' : '测试账号'}
              </Button>
              <p className="text-xs leading-5 text-muted-foreground">{quickTestMessage || '建议先用短 prompt 验证账号是否 READY。'}</p>
            </div>
          </InfoCard>

          <JsonPreview title="账号测试输出" value={quickTestOutput} minHeight={320} />
        </aside>
      </div>
    </div>
  );
}
