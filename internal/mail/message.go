package mail

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/quotedprintable"
	"net/textproto"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/jhillyerd/enmime"

	"github.com/MaksimZadar/temail/internal/model"
)

// headerFetchOptions asks for everything a list row needs. BODY.PEEK keeps
// \Seen unchanged. The header-field section fetches only References, which is
// not part of the ENVELOPE.
func headerFetchOptions() *imap.FetchOptions {
	return &imap.FetchOptions{
		UID:      true,
		Flags:    true,
		Envelope: true,
		BodySection: []*imap.FetchItemBodySection{{
			Specifier:    imap.PartSpecifierHeader,
			HeaderFields: []string{"References"},
			Peek:         true,
		}},
	}
}

// messageFetchOptions asks for the whole message (BODY.PEEK[]) and its
// BODYSTRUCTURE. The structure gives the IMAP part numbers for attachments.
func messageFetchOptions() *imap.FetchOptions {
	return &imap.FetchOptions{
		UID:           true,
		Flags:         true,
		Envelope:      true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		BodySection:   []*imap.FetchItemBodySection{{Peek: true}},
	}
}

// searchUIDs returns the UIDs in rng, ascending. "n:*" always matches the
// highest UID even when it is below n, so results are filtered to the range.
func searchUIDs(cl *imapclient.Client, rng UIDRange) ([]imap.UID, error) {
	first := rng.First
	if first == 0 {
		first = 1
	}
	if rng.Last != 0 && rng.Last < first {
		return nil, nil
	}
	crit := &imap.SearchCriteria{
		UID: []imap.UIDSet{{{Start: imap.UID(first), Stop: imap.UID(rng.Last)}}},
	}
	data, err := cl.UIDSearch(crit, nil).Wait()
	if err != nil {
		return nil, err
	}
	var out []imap.UID
	for _, uid := range data.AllUIDs() {
		if uint32(uid) < first || (rng.Last != 0 && uint32(uid) > rng.Last) {
			continue
		}
		out = append(out, uid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// headerFromBuffer builds a MessageHeader from a FETCH response.
func headerFromBuffer(mailbox string, uidValidity uint32, buf *imapclient.FetchMessageBuffer) model.MessageHeader {
	h := model.MessageHeader{
		Mailbox:     mailbox,
		UIDValidity: uidValidity,
		UID:         uint32(buf.UID),
		Flags:       flagStrings(buf.Flags),
	}
	if env := buf.Envelope; env != nil {
		h.MessageID = env.MessageID
		h.Subject = env.Subject
		h.From = addresses(env.From)
		h.To = addresses(env.To)
		h.Date = env.Date
		h.InReplyTo = strings.Join(env.InReplyTo, " ")
	}
	if sec := buf.FindBodySection(&imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: []string{"References"},
	}); sec != nil {
		h.References = headerField(sec, "References")
	}
	return h
}

// messageFromBuffer builds a Message from a full-message FETCH response. The
// HTML body is sanitised and the text body is always kept.
func messageFromBuffer(mailbox string, uidValidity uint32, buf *imapclient.FetchMessageBuffer) (model.Message, error) {
	raw := buf.FindBodySection(&imap.FetchItemBodySection{})
	if raw == nil {
		return model.Message{}, errors.New("server returned no message body")
	}
	hdr := headerFromBuffer(mailbox, uidValidity, buf)
	hdr.References = headerField(raw, "References")

	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return model.Message{}, err
	}
	text := env.Text
	if strings.TrimSpace(text) == "" && env.HTML != "" {
		text = htmlToText(env.HTML)
	}
	return model.Message{
		MessageHeader: hdr,
		TextBody:      text,
		HTMLBody:      sanitizeHTML(env.HTML),
		Attachments:   attachmentsOf(buf.BodyStructure),
	}, nil
}

// headerField returns the first value of key from a header block (a full
// message or a HEADER.FIELDS section). Unparseable input yields "".
func headerField(section []byte, key string) string {
	h, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(section))).ReadMIMEHeader()
	if err != nil && len(h) == 0 {
		return ""
	}
	return h.Get(key)
}

func addresses(in []imap.Address) []model.Address {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.Address, 0, len(in))
	for _, a := range in {
		email := a.Mailbox
		if a.Host != "" {
			email += "@" + a.Host
		}
		out = append(out, model.Address{Name: a.Name, Email: email})
	}
	return out
}

func flagStrings(flags []imap.Flag) []string {
	if len(flags) == 0 {
		return nil
	}
	out := make([]string, 0, len(flags))
	for _, f := range flags {
		out = append(out, string(f))
	}
	return out
}

// partRef is one leaf MIME part with its IMAP section number ("1", "2.1", ...).
type partRef struct {
	id   string
	part *imap.BodyStructureSinglePart
}

// indexParts lists the leaf parts of a BODYSTRUCTURE in order. A single-part
// message is reported as part "1", matching IMAP's BODY[1] for that message.
func indexParts(bs imap.BodyStructure) []partRef {
	var out []partRef
	bs.Walk(func(path []int, part imap.BodyStructure) bool {
		if sp, ok := part.(*imap.BodyStructureSinglePart); ok {
			out = append(out, partRef{id: joinPath(path), part: sp})
		}
		return true
	})
	return out
}

func findPart(parts []partRef, id string) (partRef, bool) {
	for _, p := range parts {
		if p.id == id {
			return p, true
		}
	}
	return partRef{}, false
}

func joinPath(path []int) string {
	s := make([]string, len(path))
	for i, n := range path {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ".")
}

// partIDPattern accepts IMAP section numbers only: dotted positive integers,
// with a bounded depth and length, so a part ID from the UI cannot inject IMAP
// syntax.
var partIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,3}(\.[1-9][0-9]{0,3}){0,7}$`)

// parsePartID validates an IMAP part number and returns its path.
func parsePartID(id string) ([]int, bool) {
	if !partIDPattern.MatchString(id) {
		return nil, false
	}
	parts := strings.Split(id, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

// isAttachment reports whether a leaf part is an attachment rather than a body
// part. A text/plain or text/html part is a body unless it has a file name or
// an attachment disposition. Every other leaf is treated as an attachment.
func isAttachment(sp *imap.BodyStructureSinglePart) bool {
	if d := sp.Disposition(); d != nil && strings.EqualFold(d.Value, "attachment") {
		return true
	}
	if filenameOf(sp) != "" {
		return true
	}
	isText := strings.EqualFold(sp.Type, "text")
	isBody := strings.EqualFold(sp.Subtype, "plain") || strings.EqualFold(sp.Subtype, "html")
	return !(isText && isBody)
}

// attachmentsOf lists the attachment parts of a message.
func attachmentsOf(bs imap.BodyStructure) []model.Attachment {
	if bs == nil {
		return nil
	}
	var out []model.Attachment
	for _, ref := range indexParts(bs) {
		if !isAttachment(ref.part) {
			continue
		}
		out = append(out, model.Attachment{
			Filename:    filenameOf(ref.part),
			ContentType: strings.ToLower(ref.part.Type + "/" + ref.part.Subtype),
			Size:        int64(ref.part.Size),
			PartID:      ref.id,
		})
	}
	return out
}

// filenameOf returns a display-safe file name: RFC 2047 decoded and without
// directory components.
func filenameOf(sp *imap.BodyStructureSinglePart) string {
	name := ""
	if d := sp.Disposition(); d != nil {
		name = paramValue(d.Params, "filename")
	}
	if name == "" {
		name = paramValue(sp.Params, "name")
	}
	if name == "" {
		return ""
	}
	if dec, err := wordDecoder.DecodeHeader(name); err == nil {
		name = dec
	}
	name = strings.ReplaceAll(name, "\\", "/")
	return path.Base(name)
}

func paramValue(m map[string]string, key string) string {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// decodeTransferEncoding undoes the Content-Transfer-Encoding of an attachment
// so the caller receives the file's bytes.
func decodeTransferEncoding(enc string, raw []byte) ([]byte, error) {
	var (
		out []byte
		err error
	)
	switch strings.ToUpper(enc) {
	case "BASE64":
		out, err = io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(raw)))
	case "QUOTED-PRINTABLE":
		out, err = io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
	default:
		out = raw
	}
	if err != nil {
		return nil, fmt.Errorf("decode attachment (%s): %w", enc, err)
	}
	return out, nil
}
