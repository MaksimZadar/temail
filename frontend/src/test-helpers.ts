import type { MessageHeader } from "./lib/api";

export const hdr = (
  uid: number,
  o: Partial<MessageHeader> = {},
): MessageHeader => ({
  mailbox: "INBOX",
  uidValidity: 7,
  uid,
  messageId: `<${uid}@x>`,
  subject: `Subject ${uid}`,
  from: [{ name: "Ana", email: "ana@example.com" }],
  to: null,
  date: new Date(2026, 0, 1, 0, 0, uid).toISOString(),
  flags: [],
  ...o,
});
