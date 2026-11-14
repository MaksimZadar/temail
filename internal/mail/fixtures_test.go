package mail

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// crlf converts a test message written with LF line endings to CRLF, as IMAP
// servers store it.
func crlf(s string) []byte {
	return []byte(strings.ReplaceAll(s, "\n", "\r\n"))
}

// altMessage is multipart/alternative with a text and an HTML part, plus
// threading headers.
func altMessage() []byte {
	return crlf(`From: Alice <alice@example.org>
To: Bob <bob@example.org>
Subject: Alt body
Message-ID: <alt-1@example.org>
In-Reply-To: <parent@example.org>
References: <root@example.org> <parent@example.org>
Date: Mon, 02 Nov 2026 10:00:00 +0000
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="ALT"

--ALT
Content-Type: text/plain; charset=utf-8

Plain hello.
--ALT
Content-Type: text/html; charset=utf-8

<html><body><p>HTML <b>hello</b></p></body></html>
--ALT--
`)
}

// remoteImageMessage is a single HTML part with a script, event handlers, a
// javascript: link, a remote tracking pixel, a cid: image and a style block.
func remoteImageMessage() []byte {
	return crlf(`From: Newsletter <news@example.org>
To: Bob <bob@example.org>
Subject: Remote image
Message-ID: <remote-1@example.org>
Date: Mon, 02 Nov 2026 11:00:00 +0000
MIME-Version: 1.0
Content-Type: text/html; charset=utf-8

<html><head><script>alert('x')</script><style>p{color:red}</style></head><body onload="evil()"><p onclick="x()">Hi <a href="javascript:alert(1)">bad link</a> <a href="https://example.org/ok">good link</a></p><img src="https://tracker.example/pixel.gif" alt="pixel"><img src="cid:logo@example.org" alt="logo"></body></html>
`)
}

// pdfPayload is the decoded content of the attachment in attachmentMessage.
var pdfPayload = []byte("%PDF-1.4 fake attachment payload")

// notesText is the decoded content of the quoted-printable attachment.
const notesText = "caf\u00e9 au lait"

// attachmentMessage is multipart/mixed: a text body, a base64 PDF and a
// quoted-printable text file. Part numbers: body 1, pdf 2, notes 3.
func attachmentMessage() []byte {
	pdf := base64.StdEncoding.EncodeToString(pdfPayload)
	return crlf(fmt.Sprintf(`From: Carol <carol@example.org>
To: Bob <bob@example.org>
Subject: With attachment
Message-ID: <att-1@example.org>
Date: Mon, 02 Nov 2026 12:00:00 +0000
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="MIX"

--MIX
Content-Type: text/plain; charset=utf-8

See attached.
--MIX
Content-Type: application/pdf; name="report.pdf"
Content-Disposition: attachment; filename="report.pdf"
Content-Transfer-Encoding: base64

%s
--MIX
Content-Type: text/plain; charset=utf-8; name="notes.txt"
Content-Disposition: attachment; filename="notes.txt"
Content-Transfer-Encoding: quoted-printable

caf=C3=A9 au lait
--MIX--
`, pdf))
}

// plainMessage is a minimal text/plain message with the given number in its
// subject.
func plainMessage(n int) []byte {
	return crlf(fmt.Sprintf(`From: Sender <sender@example.org>
To: Bob <bob@example.org>
Subject: msg %d
Message-ID: <msg-%d@example.org>
Date: Mon, 02 Nov 2026 09:00:00 +0000
MIME-Version: 1.0
Content-Type: text/plain; charset=utf-8

Body %d.
`, n, n, n))
}
