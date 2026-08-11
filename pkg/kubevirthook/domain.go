package kubevirthook

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
)

// Convert renders every attachment into the domain XML and returns the complete
// document, which is what virt-launcher expects back from OnDefineDomain.
//
// The document is edited by splicing bytes, not by decoding and re-encoding it.
// Re-encoding would round-trip the whole domain through encoding/xml, which
// rewrites namespace prefixes (KubeVirt's <qemu:commandline> would come back as
// a default-namespaced element) and drops the formatting; splicing leaves every
// byte this hook does not own exactly as KubeVirt wrote it.
func Convert(domainXML []byte, attachments []Attachment) ([]byte, error) {
	if len(attachments) == 0 {
		return domainXML, nil
	}

	layout, err := parseDomain(domainXML)
	if err != nil {
		return nil, err
	}
	if layout.devicesEnd < 0 {
		return nil, fmt.Errorf("domain has no <devices> element")
	}

	edits, err := layout.shareMemory()
	if err != nil {
		return nil, err
	}
	for _, att := range attachments {
		existing := layout.iface(att.Name)
		if existing == nil {
			// Nothing to replace: the plugin registers no domainAttachmentType,
			// so KubeVirt generated no element and the device is appended.
			edits = append(edits, edit{at: layout.devicesEnd, text: interfaceXML(att)})
			continue
		}
		if att.MAC == "" {
			// KubeVirt already pinned a MAC on the element it generated;
			// keeping it means the guest does not see the NIC change identity.
			att.MAC = existing.mac
		}
		// The MAC is the only thing carried over. The rest of a generated
		// element describes a tap backend that contradicts a vhost-user source,
		// and the replacement therefore also drops any <address> libvirt had
		// assigned, letting it allocate a fresh PCI slot. That is invisible in
		// the supported configuration -- the binding plugin registers no
		// domainAttachmentType, so KubeVirt generates no element and this branch
		// never runs -- but if a future KubeVirt does generate one, the guest
		// would see the NIC move slots across a redefine.
		edits = append(edits, edit{at: existing.start, until: existing.end, text: interfaceXML(att)})
	}

	return splice(domainXML, edits)
}

// edit replaces domainXML[at:until] with text; an edit with until == 0 inserts
// text at "at" instead.
type edit struct {
	at, until int
	text      string
}

// splice applies edits to the document in offset order. Everything the edits do
// not cover is copied through byte for byte.
func splice(doc []byte, edits []edit) ([]byte, error) {
	slices.SortStableFunc(edits, func(a, b edit) int { return a.at - b.at })

	var out bytes.Buffer
	cursor := 0
	for _, e := range edits {
		if e.at < cursor {
			return nil, fmt.Errorf("overlapping edits in the domain at offset %d", e.at)
		}
		out.Write(doc[cursor:e.at])
		out.WriteString(e.text)
		cursor = e.at
		if e.until > 0 {
			cursor = e.until
		}
	}
	out.Write(doc[cursor:])
	return out.Bytes(), nil
}

// interfaceXML renders a complete vhost-user interface element.
//
// The element is written from scratch even when it replaces one KubeVirt
// generated: a generated element carries a tap backend (<source>, <target>,
// <script>, <driver>) that contradicts a vhost-user source, and keeping any of
// it would leave the guest with a NIC backed by two things at once.
func interfaceXML(att Attachment) string {
	mac := ""
	if att.MAC != "" {
		mac = "<mac address='" + escapeAttr(att.MAC) + "'/>"
	}
	return "<interface type='vhostuser'>" +
		"<alias name='ua-" + escapeAttr(att.Name) + "'/>" +
		mac +
		"<source type='unix' path='" + escapeAttr(att.SocketPath) +
		"' mode='" + escapeAttr(att.Mode) + "'/>" +
		"<model type='virtio'/>" +
		"<driver queues='1'/>" +
		"</interface>"
}

// escapeAttr escapes a value for use inside a single-quoted XML attribute.
//
// xml.EscapeText only fails when the writer does, and a bytes.Buffer never
// does, so the error is discarded rather than propagated through every caller.
func escapeAttr(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// domainLayout records where in the raw document the interesting elements sit.
type domainLayout struct {
	interfaces []ifaceRegion
	// devicesEnd is the offset of the </devices> tag, i.e. where a new
	// interface has to be inserted.
	devicesEnd int
	// memoryBackingEnd is the offset of the </memoryBacking> tag, i.e. where a
	// missing <access> element has to be inserted; -1 when the domain has no
	// <memoryBacking> at all.
	memoryBackingEnd int
	// access is the <memoryBacking><access> element, when there is one.
	access *accessRegion
}

type accessRegion struct {
	shared     bool
	start, end int
}

type ifaceRegion struct {
	alias      string
	mac        string
	start, end int
}

// shareMemory returns the edits that make the guest's memory shareable.
//
// vhost-user works by letting the CRA map the guest's virtio rings, so without
// shared memory the socket connects and no packet ever moves -- the worst kind
// of failure, because everything looks healthy. Setting it is this hook's job:
// KubeVirt emits <access mode='shared'/> only for a virtiofs guest, and gives a
// hugepage-backed one a memfd backing with no access mode at all, which libvirt
// then defaults to private.
//
// Hugepages are still required from the VM, because they are what makes
// KubeVirt emit the memfd <memoryBacking> in the first place; there is nothing
// to share without them.
func (l *domainLayout) shareMemory() ([]edit, error) {
	if l.memoryBackingEnd < 0 {
		return nil, fmt.Errorf("domain has no <memoryBacking>: vhost-user needs " +
			"shareable guest memory, which KubeVirt only emits for a guest with " +
			"hugepages configured")
	}
	switch {
	case l.access == nil:
		return []edit{{at: l.memoryBackingEnd, text: "<access mode='shared'/>"}}, nil
	case l.access.shared:
		return nil, nil
	default:
		return []edit{{at: l.access.start, until: l.access.end, text: "<access mode='shared'/>"}}, nil
	}
}

func (l *domainLayout) iface(name string) *ifaceRegion {
	// KubeVirt names every device alias after the VMI object it came from, as
	// "ua-<name>"; libvirt reports it back with that same prefix. Both
	// spellings are accepted so the hook does not depend on which side
	// generated the element.
	for i := range l.interfaces {
		if l.interfaces[i].alias == name || l.interfaces[i].alias == "ua-"+name {
			return &l.interfaces[i]
		}
	}
	return nil
}

//nolint:gocognit // one flat token walk; splitting it would only hide the state
func parseDomain(domainXML []byte) (*domainLayout, error) {
	layout := &domainLayout{devicesEnd: -1, memoryBackingEnd: -1}
	dec := xml.NewDecoder(bytes.NewReader(domainXML))

	var stack []string
	var current *ifaceRegion
	var access *accessRegion

	for {
		start := int(dec.InputOffset())
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing domain XML: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			switch {
			case current != nil:
				// Inside an interface: pick up the identity of the element so
				// it can be matched and its MAC carried over.
				switch t.Name.Local {
				case "alias":
					current.alias = attr(t, "name")
				case "mac":
					current.mac = attr(t, "address")
				}
			case at(stack, "domain", "devices", "interface"):
				current = &ifaceRegion{start: start, end: -1}
			case at(stack, "domain", "memoryBacking", "access"):
				access = &accessRegion{shared: attr(t, "mode") == "shared", start: start}
			}
		case xml.EndElement:
			if current != nil && at(stack, "domain", "devices", "interface") {
				current.end = int(dec.InputOffset())
				layout.interfaces = append(layout.interfaces, *current)
				current = nil
			}
			if at(stack, "domain", "devices") {
				layout.devicesEnd = start
			}
			if access != nil && at(stack, "domain", "memoryBacking", "access") {
				access.end = int(dec.InputOffset())
				layout.access = access
				access = nil
			}
			if at(stack, "domain", "memoryBacking") {
				layout.memoryBackingEnd = start
			}
			stack = stack[:len(stack)-1]
		}
	}

	if len(stack) != 0 {
		return nil, fmt.Errorf("parsing domain XML: unclosed <%s>", stack[len(stack)-1])
	}
	return layout, nil
}

// at reports whether the element stack is exactly the given path. Requiring the
// full path is what keeps an <interface> nested somewhere else (a hostdev's, a
// snapshot's) from being mistaken for a device.
func at(stack []string, path ...string) bool {
	if len(stack) != len(path) {
		return false
	}
	for i := range path {
		if stack[i] != path[i] {
			return false
		}
	}
	return true
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
