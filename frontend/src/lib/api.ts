// Thin typed wrappers over the generated Wails bindings and events. Tests
// replace this whole module, so nothing else imports the bindings directly.
import { Browser, Events } from "@wailsio/runtime";
import {
  ActionService,
  LLMService,
  MailService,
  SetupService,
  SuggestionService,
  SyncService,
} from "../../bindings/github.com/MaksimZadar/temail/internal/services";

export interface SetupStatus {
  configured: boolean;
  keyringOk: boolean;
  keyringMessage: string;
  bridgeReachable: boolean;
  certPinned: boolean;
  accountId: string;
}
export interface CertProbe {
  fingerprint: string;
  subject: string;
}
export interface ConnectionTest {
  ok: boolean;
  category: string;
  message: string;
}
export interface SyncResult {
  mailboxes: number;
  headersFetched: number;
  deleted: number;
  bodiesFetched: number;
  bodiesFailed: number;
  mailboxErrors: { mailbox: string; message: string }[] | null;
}
export interface SyncStatus {
  /** True only during the initial sync. Use `watching` for the live state. */
  running: boolean;
  /** The change watcher is up (after the initial sync). */
  watching: boolean;
  /** The IDLE connection is up; false means the watcher is polling. */
  idle: boolean;
  lastChecked: string | null;
  stopTimedOut: boolean;
  lastResult: SyncResult | null;
  lastError: string;
}
export interface MailboxInfo {
  name: string;
  displayName: string;
  messages: number;
}
export type MailboxKind =
  | "inbox"
  | "sent"
  | "drafts"
  | "trash"
  | "spam"
  | "archive"
  | "folder"
  | "label";
export interface MailboxDTO extends MailboxInfo {
  kind: MailboxKind;
  /** Unseen messages, from the last sync. */
  unseen: number;
  uidValidity: number;
  bodies: number;
  lastSync: string | null;
}
export interface Address {
  name: string;
  email: string;
}
/** A mailbox a message is in: its folder or system mailbox, or a label. */
export interface MailboxRef {
  name: string;
  displayName: string;
  kind: MailboxKind;
}
export interface MessageHeader {
  internalId?: string;
  canonical?: boolean;
  mailboxes?: MailboxRef[] | null;
  mailbox: string;
  uidValidity: number;
  uid: number;
  messageId: string;
  subject: string;
  from: Address[] | null;
  to: Address[] | null;
  date: string;
  flags: string[] | null;
}
export interface Attachment {
  filename: string;
  contentType: string;
  size: number;
  partId: string;
}
export interface Message extends MessageHeader {
  textBody: string;
  htmlBody: string;
  attachments: Attachment[] | null;
}
export interface PageCursor {
  date: string;
  uid: number;
}
export interface MessagePage {
  messages: MessageHeader[] | null;
  next: PageCursor | null;
}
export interface CacheInfo {
  bytes: number;
  messages: number;
  bodies: number;
}
export interface LLMCapabilities {
  structuredOutput: boolean;
  tools: boolean;
  maxContext: number;
}
export interface LLMProvider {
  name: string;
  baseURL: string;
  model: string;
  kind: "local" | "cloud";
  enabled: boolean;
  capabilities: LLMCapabilities;
  isDefault: boolean;
  hasKey: boolean;
}
/** What SaveProvider takes. apiKey is write-only; empty keeps the stored key. */
export interface LLMProviderIn {
  name: string;
  baseURL: string;
  model: string;
  kind: "local" | "cloud";
  enabled: boolean;
  capabilities: LLMCapabilities;
  apiKey: string;
}
export interface ProviderTest {
  ok: boolean;
  category: string;
  message: string;
}
export interface AttachmentData {
  filename: string;
  contentType: string;
  size: number;
  /** base64 */
  data: string;
}
export interface ProgressEvent {
  state: string;
  mailbox: string;
  headersDone: number;
  headersTotal: number;
  bodiesDone: number;
  bodiesTotal: number;
}

export type ActionType =
  | "mark_read"
  | "mark_unread"
  | "star"
  | "unstar"
  | "archive"
  | "move"
  | "trash"
  | "spam"
  | "not_spam"
  | "add_label"
  | "remove_label";
export interface ActionIn {
  type: ActionType;
  params: { folder?: string; label?: string };
  messageIds: string[];
}
export interface ActionFailure {
  mid: string;
  mailbox: string;
  message: string;
  code?: string;
}
export interface ActionResult {
  applied: string[] | null;
  failed: ActionFailure[] | null;
  appliedCount: number;
  failedCount: number;
  batchId?: string;
  /** Audit rows that can be undone (partial or unverified ones are left out). */
  auditIds?: number[] | null;
  /** Set by Undo/UndoLast refusals; empty for a normal result. */
  code?: UndoCode | "";
}
export type UndoCode =
  | "not_undoable"
  | "already_undone"
  | "not_found"
  | "wrong_account"
  | "diverged";
/** One row per action batch; count is the number of messages. */
export interface AuditEntry {
  id: number;
  ts: string;
  type: string;
  params: { folder?: string; label?: string };
  count: number;
  error: string;
  undone: boolean;
  batchId?: string;
  auditIds?: number[] | null;
  /** Why the batch can't be undone; empty when it can. */
  code?: UndoCode | "";
}
export interface ActionsDone {
  type: string;
  count: number;
  failed: number;
}
export const applyAction = (a: ActionIn): Promise<ActionResult> =>
  ActionService.Apply(a as never) as unknown as Promise<ActionResult>;
export const undoAction = (id: number): Promise<ActionResult> =>
  ActionService.Undo(id) as unknown as Promise<ActionResult>;
export const undoLast = (): Promise<ActionResult> =>
  ActionService.UndoLast() as unknown as Promise<ActionResult>;
export const recentActions = (limit: number): Promise<AuditEntry[]> =>
  ActionService.Recent(limit) as unknown as Promise<AuditEntry[]>;

export interface SuggestLabel {
  name: string;
  confidence: number;
  reason: string;
  /** False means the label doesn't exist yet and will be created. */
  existing: boolean;
  /** label: add an existing label; folder: move to that folder; new: create a label. */
  target: "label" | "folder" | "new";
}
export interface SuggestResult {
  jobId: string;
  mid: string;
  ok: boolean;
  labels: SuggestLabel[] | null;
  summary: string;
  error: string;
  code: string;
}
export interface SuggestDone {
  jobId: string;
  total: number;
  failed: number;
}
export interface SuggestJob {
  jobId: string;
  provider: string;
  state: "running" | "done" | "cancelled";
  total: number;
  completed: number;
  failed: number;
  results: SuggestResult[] | null;
}
export interface SuggestApply {
  applied: string[] | null;
  failed: ActionFailure[] | null;
  appliedCount: number;
  failedCount: number;
  batchIds: string[] | null;
  auditIds: number[] | null;
  createdLabels: string[] | null;
}
/** An empty provider name uses the default (local preferred). */
export const suggest = (mids: string[], provider: string): Promise<string> =>
  SuggestionService.Suggest(mids, provider);
export const suggestStatus = (jobId: string): Promise<SuggestJob> =>
  SuggestionService.Status(jobId) as unknown as Promise<SuggestJob>;
export const suggestCancel = (jobId: string): Promise<void> =>
  SuggestionService.Cancel(jobId);
export const applySuggestions = (
  jobId: string,
  accepted: Record<string, string[]>,
): Promise<SuggestApply> =>
  SuggestionService.ApplySuggestions(
    jobId,
    accepted,
  ) as unknown as Promise<SuggestApply>;

export const getStatus = (): Promise<SetupStatus> => SetupService.GetStatus();
export const saveAccount = (
  host: string,
  imapPort: number,
  smtpPort: number,
  tlsMode: string,
  username: string,
  password: string,
): Promise<void> =>
  SetupService.SaveAccount(
    host,
    imapPort,
    smtpPort,
    tlsMode,
    username,
    password,
  );
export const probeCert = (): Promise<CertProbe> => SetupService.ProbeCert();
export const trustCert = (
  fingerprint: string,
  replace: boolean,
): Promise<void> => SetupService.TrustCert(fingerprint, replace);
export const testConnection = (): Promise<ConnectionTest> =>
  SetupService.TestConnection();
export const listMailboxes = (): Promise<MailboxDTO[]> =>
  MailService.ListMailboxes() as unknown as Promise<MailboxDTO[]>;
export const listMessages = (
  mailbox: string,
  cursor: PageCursor | null,
  limit: number,
): Promise<MessagePage> =>
  MailService.ListMessages(
    mailbox,
    cursor as never,
    limit,
  ) as unknown as Promise<MessagePage>;
export const getMessage = (
  mailbox: string,
  uidValidity: number,
  uid: number,
): Promise<Message> =>
  MailService.GetMessage(
    mailbox,
    uidValidity,
    uid,
  ) as unknown as Promise<Message>;
export const getAttachment = (
  mailbox: string,
  uidValidity: number,
  uid: number,
  partId: string,
): Promise<AttachmentData> =>
  MailService.GetAttachment(
    mailbox,
    uidValidity,
    uid,
    partId,
  ) as unknown as Promise<AttachmentData>;
export const cacheInfo = (): Promise<CacheInfo> =>
  MailService.CacheInfo() as unknown as Promise<CacheInfo>;
export const listProviders = (): Promise<LLMProvider[]> =>
  LLMService.ListProviders() as unknown as Promise<LLMProvider[]>;
export const saveProvider = (input: LLMProviderIn): Promise<void> =>
  LLMService.SaveProvider(input as never);
export const deleteProvider = (name: string): Promise<void> =>
  LLMService.DeleteProvider(name);
/** An empty name clears the default. */
export const setDefaultProvider = (name: string): Promise<void> =>
  LLMService.SetDefaultProvider(name);
export const testProvider = (name: string): Promise<ProviderTest> =>
  LLMService.TestProvider(name) as unknown as Promise<ProviderTest>;
export const listModels = (name: string): Promise<string[]> =>
  LLMService.ListModels(name) as unknown as Promise<string[]>;
/** Open a URL in the system browser. */
export const openUrl = (url: string): Promise<void> => Browser.OpenURL(url);
export const syncStart = (): Promise<void> => SyncService.Start();
export const syncStatus = (): Promise<SyncStatus> => SyncService.Status();

/** Subscribe to a Go event. Returns an unsubscribe function. */
export function onEvent<T>(name: string, fn: (data: T) => void): () => void {
  return Events.On(name, (e: { data: T }) => fn(e.data));
}
