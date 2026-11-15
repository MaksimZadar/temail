// Thin typed wrappers over the generated Wails bindings and events. Tests
// replace this whole module, so nothing else imports the bindings directly.
import { Events } from "@wailsio/runtime";
import {
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
  running: boolean;
  stopTimedOut: boolean;
  lastResult: SyncResult | null;
  lastError: string;
}
export interface MailboxInfo {
  name: string;
  displayName: string;
  messages: number;
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
export const listMailboxes = (): Promise<MailboxInfo[]> =>
  MailService.ListMailboxes();
export const syncStart = (): Promise<void> => SyncService.Start();
export const syncStatus = (): Promise<SyncStatus> => SyncService.Status();

/** Subscribe to a Go event. Returns an unsubscribe function. */
export function onEvent<T>(name: string, fn: (data: T) => void): () => void {
  return Events.On(name, (e: { data: T }) => fn(e.data));
}
