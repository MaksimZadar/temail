import { safeUrl, type MailLink } from "./links";

/** The CSP of the message frame: no scripts, no network, only data: images. */
export const FRAME_CSP =
  "default-src 'none'; style-src 'unsafe-inline'; img-src data:";

const DROP =
  "script,iframe,frame,object,embed,form,meta,base,link,style[media]";

export interface Prepared {
  html: string;
  links: MailLink[];
}

/**
 * Defensive second pass over HTML that Go already sanitised. It runs in an
 * inert DOM (nothing loads or runs). Every link is turned into plain text so
 * nothing in the frame can navigate; the real targets go to a list that the
 * host shows, so a click is always an explicit host action.
 */
export function prepareHtml(html: string): Prepared {
  const doc = new DOMParser().parseFromString(html, "text/html");
  doc.querySelectorAll(DROP).forEach((n) => n.remove());
  const links: MailLink[] = [];
  doc.querySelectorAll("*").forEach((el) => {
    for (const a of [...el.attributes])
      if (a.name.startsWith("on")) el.removeAttribute(a.name);
  });
  doc.querySelectorAll("a").forEach((a) => {
    const href = a.getAttribute("href");
    const url = href ? safeUrl(href) : null;
    a.removeAttribute("href");
    a.removeAttribute("target");
    a.removeAttribute("ping");
    if (url) {
      links.push({ text: (a.textContent ?? "").trim(), href: url });
      a.setAttribute("data-link", String(links.length));
    }
  });
  return { html: doc.body.innerHTML, links };
}

const FRAME_STYLE = `
  html { color-scheme: light; }
  body { margin: 0; padding: 16px 20px 32px; background: #fff; color: #1d1f23;
         font: 14px/1.55 system-ui, -apple-system, "Segoe UI", sans-serif;
         overflow-wrap: anywhere; }
  img { max-width: 100%; height: auto; }
  table { max-width: 100%; }
  a[data-link] { color: #6d41d8; text-decoration: underline; cursor: default; }
  blockquote { margin: 0 0 0 4px; padding-left: 12px; border-left: 3px solid #d8d3ee; color: #555; }
  pre { white-space: pre-wrap; }
`;

/** The full document for the sandboxed frame's `srcdoc`. */
export function buildSrcdoc(bodyHtml: string): string {
  return (
    `<!doctype html><html><head><meta charset="utf-8">` +
    `<meta http-equiv="Content-Security-Policy" content="${FRAME_CSP}">` +
    `<style>${FRAME_STYLE}</style></head><body>${bodyHtml}</body></html>`
  );
}

/** Readable plain text from HTML, for the "plain text" view when there is no text part. */
export function htmlToText(html: string): string {
  const doc = new DOMParser().parseFromString(html, "text/html");
  doc.querySelectorAll("script,style").forEach((n) => n.remove());
  doc.querySelectorAll("br").forEach((n) => n.replaceWith("\n"));
  doc
    .querySelectorAll("p,div,li,tr,h1,h2,h3,h4,h5,h6,blockquote")
    .forEach((n) => n.append("\n"));
  return (doc.body.textContent ?? "").replace(/\n{3,}/g, "\n\n").trim();
}
