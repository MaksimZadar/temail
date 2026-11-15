// Thin typed wrappers over the generated Wails bindings and events. Tests
// replace this whole module, so nothing else imports the bindings directly.
import { Browser, Events } from "@wailsio/runtime";
import {
  LLMService,
  MailService,
  SetupService,
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
