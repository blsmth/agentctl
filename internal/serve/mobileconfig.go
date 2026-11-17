package serve

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
)

// MobileconfigContentType makes iOS Safari offer to install the profile.
const MobileconfigContentType = "application/x-apple-aspen-config"

// Mobileconfig returns an unsigned iOS configuration profile whose single
// payload (com.apple.security.root) installs the CA certificate. The UUIDs
// are derived from the certificate, so re-downloading the profile for the
// same CA replaces rather than duplicates it on the phone.
func Mobileconfig(ca *CA) []byte {
	der := ca.Cert.Raw
	name := ca.Cert.Subject.CommonName
	certUUID := derivedUUID(der, "payload")
	profileUUID := derivedUUID(der, "profile")

	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	b.WriteString("<key>PayloadContent</key>\n<array>\n<dict>\n")
	plistString(&b, "PayloadCertificateFileName", "agentctl-ca.cer")
	fmt.Fprintf(&b, "<key>PayloadContent</key>\n<data>%s</data>\n", base64.StdEncoding.EncodeToString(der))
	plistString(&b, "PayloadDescription", "Trusts the agentctl serve certificate authority on this Mac.")
	plistString(&b, "PayloadDisplayName", name)
	plistString(&b, "PayloadIdentifier", "com.blsmth.agentctl.ca."+certUUID)
	plistString(&b, "PayloadType", "com.apple.security.root")
	plistString(&b, "PayloadUUID", certUUID)
	b.WriteString("<key>PayloadVersion</key>\n<integer>1</integer>\n")
	b.WriteString("</dict>\n</array>\n")
	plistString(&b, "PayloadDescription", "Installs the certificate authority for agentctl serve. After installing, enable full trust in Settings > General > About > Certificate Trust Settings.")
	plistString(&b, "PayloadDisplayName", "agentctl: "+name)
	plistString(&b, "PayloadIdentifier", "com.blsmth.agentctl.profile."+profileUUID)
	b.WriteString("<key>PayloadRemovalDisallowed</key>\n<false/>\n")
	plistString(&b, "PayloadType", "Configuration")
	plistString(&b, "PayloadUUID", profileUUID)
	b.WriteString("<key>PayloadVersion</key>\n<integer>1</integer>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

func plistString(b *bytes.Buffer, key, val string) {
	b.WriteString("<key>")
	_ = xml.EscapeText(b, []byte(key))
	b.WriteString("</key>\n<string>")
	_ = xml.EscapeText(b, []byte(val))
	b.WriteString("</string>\n")
}

// derivedUUID returns a stable RFC 4122 (version 4 layout) UUID string from
// seed and label.
func derivedUUID(seed []byte, label string) string {
	sum := sha256.Sum256(append([]byte(label+":"), seed...))
	u := sum[:16]
	u[6] = (u[6] & 0x0f) | 0x40
	u[8] = (u[8] & 0x3f) | 0x80
	return fmt.Sprintf("%X-%X-%X-%X-%X", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}
