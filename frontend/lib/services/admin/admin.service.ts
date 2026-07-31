import { apiFetch } from '@/lib/services/core/api-client';
import type {
  AccountsPayload,
  AdminConfigPayload,
  AdminVerifyPayload,
  AttachmentInput,
  ConversationDetailPayload,
  ConversationsPayload,
  DiagnosticsPayload,
  HealthPayload,
  JsonResult,
  MCPPayload,
  VersionPayload,
} from './types';

export const AdminService = {
  verify() {
    return apiFetch<AdminVerifyPayload>('/admin/verify');
  },
  login(password: string) {
    return apiFetch<JsonResult>('/admin/login', {
      method: 'POST',
      body: JSON.stringify({ password }),
    });
  },
  logout() {
    return apiFetch<JsonResult>('/admin/logout', {
      method: 'POST',
    });
  },
  getConfig() {
    return apiFetch<AdminConfigPayload>('/admin/config');
  },
  getVersion() {
    return apiFetch<VersionPayload>('/admin/version');
  },
  getHealth() {
    return apiFetch<HealthPayload>('/healthz');
  },
  getAccounts() {
    return apiFetch<AccountsPayload>('/admin/accounts');
  },
  getConversations() {
    return apiFetch<ConversationsPayload>('/admin/conversations');
  },
  getConversation(id: string) {
    return apiFetch<ConversationDetailPayload>(`/admin/conversations/${encodeURIComponent(id)}`);
  },
  deleteConversation(id: string) {
    return apiFetch<JsonResult>(`/admin/conversations/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    });
  },
  batchDeleteConversations(ids: string[]) {
    return apiFetch<JsonResult>('/admin/conversations/batch-delete', {
      method: 'POST',
      body: JSON.stringify({ ids }),
    });
  },
  testPrompt(payload: {
    prompt: string;
    model: string;
    use_web_search: boolean;
    attachments: AttachmentInput[];
    conversation_id?: string;
  }) {
    return apiFetch<JsonResult>('/admin/test', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },
  runDiagnostics(payload: { model?: string; account_email?: string; checks?: string[] }) {
    return apiFetch<DiagnosticsPayload>('/admin/diagnostics', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },
  getMCP() {
    return apiFetch<MCPPayload>('/admin/mcp');
  },
  reloadMCP() {
    return apiFetch<MCPPayload>('/admin/mcp/reload', { method: 'POST' });
  },
  callMCPTool(payload: { name: string; arguments?: Record<string, unknown> }) {
    return apiFetch<JsonResult>('/admin/mcp/call', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },
  batchAddAccounts(payload: JsonResult) {
    return apiFetch<JsonResult>('/admin/accounts/batch', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },
  updateSettings(config: JsonResult) {
    return apiFetch<JsonResult>('/admin/settings', {
      method: 'PUT',
      body: JSON.stringify({ config }),
    });
  },
  importConfig(config: JsonResult) {
    return apiFetch<JsonResult>('/admin/config/import', {
      method: 'POST',
      body: JSON.stringify({ config }),
    });
  },
  exportConfig() {
    return apiFetch<JsonResult>('/admin/config/export');
  },
  createConfigSnapshot() {
    return apiFetch<JsonResult>('/admin/config/snapshot', { method: 'POST' });
  },
  listConfigSnapshots() {
    return apiFetch<JsonResult>('/admin/config/snapshot');
  },
  startAccountLogin(email: string) {
    return apiFetch<JsonResult>('/admin/accounts/login/start', {
      method: 'POST',
      body: JSON.stringify({ email }),
    });
  },
  verifyAccountCode(email: string, code: string) {
    return apiFetch<JsonResult>('/admin/accounts/login/verify', {
      method: 'POST',
      body: JSON.stringify({ email, code }),
    });
  },
  importAccount(payload: JsonResult) {
    return apiFetch<JsonResult>('/admin/accounts/manual', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },
  quickTestAccount(payload: JsonResult) {
    return apiFetch<JsonResult>('/admin/accounts/test', {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  },
  activateAccount(email: string) {
    return apiFetch<JsonResult>('/admin/accounts/activate', {
      method: 'POST',
      body: JSON.stringify({ email }),
    });
  },
  deleteAccount(email: string) {
    return apiFetch<JsonResult>(`/admin/accounts/${encodeURIComponent(email)}`, {
      method: 'DELETE',
    });
  },
  saveAccountSettings(payload: JsonResult) {
    return apiFetch<JsonResult>('/admin/accounts', {
      method: 'PUT',
      body: JSON.stringify(payload),
    });
  },
};
