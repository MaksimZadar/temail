export interface MailLink {
  text: string;
  href: string;
}

/** Only these schemes are ever handed to the system browser. */
export function safeUrl(href: string): string | null {
  try {
    const u = new URL(href);
    return ["http:", "https:", "mailto:"].includes(u.protocol) ? u.href : null;
  } catch {
    return null;
  }
}

function hostOf(s: string): string | null {
  const t = s.trim();
  if (!t || /\s/.test(t)) return null;
  try {
    const u = new URL(/^[a-z][a-z0-9+.-]*:/i.test(t) ? t : `https://${t}`);
    if (!u.hostname.includes(".")) return null;
    return u.hostname.replace(/^www\./, "").toLowerCase();
  } catch {
    return null;
  }
}

/**
 * Ask before opening only when the link text shows an address that is not
 * where the link really goes ("paypal.com" pointing at another site). Plain
 * words like "Read more" don't need a prompt: the real address is always
 * shown in the links list first.
 */
export function needsConfirm(text: string, href: string): boolean {
  const shown = hostOf(text);
  if (!shown) return false;
  const real = hostOf(href);
  return real === null || real !== shown;
}
