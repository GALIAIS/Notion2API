export type TabKey = 'dashboard' | 'tester' | 'conversations' | 'settings' | 'accounts' | 'models';

export interface ModelItem {
  id: string;
  name?: string;
  family?: string;
  group?: string;
  notion_model?: string;
  beta?: boolean;
  enabled?: boolean;
}

export interface SessionSummary {
  user_email?: string;
  user_name?: string;
  space_name?: string;
  space_id?: string;
  probe_path?: string;
  client_version?: string;
  user_id?: string;
}

export interface SessionRefreshRuntime {
  last_refresh_at?: string;
  last_error?: string;
}

export interface FeatureConfig {
  use_web_search?: boolean;
  use_read_only_mode?: boolean;
  force_disable_upstream_edits?: boolean;
  force_fresh_thread_per_request?: boolean;
  writer_mode?: boolean;
  enable_generate_image?: boolean;
  enable_csv_attachment_support?: boolean;
  ai_surface?: string;
  thread_type?: string;
  is_custom_agent?: boolean;
  is_custom_agent_builder?: boolean;
  use_custom_agent_draft?: boolean;
  search_scopes?: string[];
  [key: string]: unknown;
}

export interface PromptConfig {
  profile?: string;
  custom_prefix?: string;
  fallback_profiles?: string[];
  max_escalation_steps?: number;
  max_refusal_retries?: number;
  cognitive_reframing_prefix?: string;
  toolbox_capability_expansion_prefix?: string;
  coding_retry_prefixes?: string[];
  general_retry_prefixes?: string[];
  direct_answer_retry_prefixes?: string[];
}

export interface AppConfigShape {
  host?: string;
  port?: number;
  api_key?: string;
  upstream_base_url?: string;
  upstream_origin?: string;
  upstream_host_header?: string;
  upstream_tls_server_name?: string;
  upstream_use_env_proxy?: boolean;
  timeout_sec?: number;
  poll_interval_sec?: number;
  poll_max_rounds?: number;
  stream_chunk_runes?: number;
  default_model?: string;
  model_id?: string;
  debug_upstream?: boolean;
  model_aliases?: Record<string, string>;
  responses?: {
    store_ttl_seconds?: number;
  };
  storage?: {
    sqlite_path?: string;
    persist_conversations?: boolean;
    persist_conversation_snapshots?: boolean;
    persist_responses?: boolean;
    persist_continuation_sessions?: boolean;
    persist_sillytavern_bindings?: boolean;
  };
  admin?: {
    password?: string;
    token_ttl_hours?: number;
  };
  login_helper?: {
    sessions_dir?: string;
    timeout_sec?: number;
  };
  prompt?: PromptConfig;
  features?: FeatureConfig;
}

export interface AdminConfigPayload {
  success: boolean;
  config: AppConfigShape;
  secrets?: {
    api_key_set?: boolean;
    admin_password_set?: boolean;
  };
  session?: SessionSummary;
  session_refresh_runtime?: SessionRefreshRuntime;
  models?: ModelItem[];
}

export interface AdminVerifyPayload {
  authenticated?: boolean;
  password_required?: boolean;
  password_configured?: boolean;
  admin_enabled?: boolean;
}

export interface VersionPayload {
  success?: boolean;
  version?: string;
  name?: string;
  default_model?: string;
  model_count?: number;
  user_email?: string;
  space_id?: string;
  features?: FeatureConfig;
  responses?: {
    store_ttl_seconds?: number;
  };
  storage?: {
    persist_conversations?: boolean;
    persist_conversation_snapshots?: boolean;
    persist_responses?: boolean;
    persist_continuation_sessions?: boolean;
    persist_sillytavern_bindings?: boolean;
  };
}

export interface HealthPayload {
  ok?: boolean;
  session_ready?: boolean;
  version?: string;
  model?: string;
  user_email?: string;
  space_id?: string;
}

export interface AccountItem {
  email?: string;
  active?: boolean;
  disabled?: boolean;
  status?: string;
  last_login_at?: string;
  last_success_at?: string;
  last_refresh_at?: string;
  last_relogin_at?: string;
  priority?: number;
  hourly_quota?: number;
  max_concurrency?: number;
  quota_limited?: boolean;
  remaining_quota?: number;
  cooldown_active?: boolean;
  cooldown_remaining_sec?: number;
  cooldown_until?: string;
  window_started_at?: string;
  window_request_count?: number;
  total_successes?: number;
  total_failures?: number;
  consecutive_failures?: number;
  user_id?: string;
  user_name?: string;
  space_id?: string;
  space_name?: string;
  plan_type?: string;
  client_version?: string;
  probe_json?: string;
  probe_exists?: boolean;
  profile_dir?: string;
  profile_dir_exists?: boolean;
  storage_state_path?: string;
  storage_state_exists?: boolean;
  pending_state_path?: string;
  pending_state_exists?: boolean;
  last_used_at?: string;
  last_error?: string;
  login_status?: {
    status?: string;
    message?: string;
    error?: string;
  };
}

export interface AccountsPayload {
  items?: AccountItem[];
  active_account?: string;
  session_ready?: boolean;
  session?: SessionSummary;
  login_helper?: {
    sessions_dir?: string;
    timeout_sec?: number;
  };
  session_refresh?: {
    enabled?: boolean;
    interval_minutes?: number;
  };
  session_refresh_runtime?: SessionRefreshRuntime;
}

export interface ConversationMessageAttachment {
  name?: string;
  content_type?: string;
  contentType?: string;
}

export interface ConversationMessage {
  id?: string;
  role?: 'user' | 'assistant' | string;
  status?: string;
  content?: string;
  created_at?: string;
  updated_at?: string;
  attachments?: ConversationMessageAttachment[];
}

export interface ConversationSummary {
  id: string;
  title?: string;
  origin?: 'local' | 'notion' | 'merged' | string;
  remote_only?: boolean;
  preview?: string;
  request_prompt?: string;
  status?: string;
  source?: string;
  transport?: string;
  model?: string;
  notion_model?: string;
  account_email?: string;
  thread_id?: string;
  trace_id?: string;
  response_id?: string;
  completion_id?: string;
  created_by_display_name?: string;
  created_at?: string;
  updated_at?: string;
  error?: string;
}

export interface ConversationDetail extends ConversationSummary {
  messages?: ConversationMessage[];
}

export interface ConversationsPayload {
  items?: ConversationSummary[];
}

export interface ConversationDetailPayload {
  item: ConversationDetail;
}

export interface JsonResult {
  [key: string]: unknown;
}

export interface AttachmentInput {
  type: 'attachment';
  name: string;
  content_type: string;
  data: string | ArrayBuffer | null;
}
