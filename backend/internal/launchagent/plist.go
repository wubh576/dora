package launchagent

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

func buildPlist(paths Paths) ([]byte, error) {
	var document bytes.Buffer
	document.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	document.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	document.WriteString("<plist version=\"1.0\">\n<dict>\n")
	writeKeyString(&document, "Label", Label)
	document.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	writeString(&document, paths.Binary, 4)
	writeString(&document, "menubar", 4)
	document.WriteString("  </array>\n")
	document.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	document.WriteString("  <key>KeepAlive</key>\n  <dict>\n    <key>SuccessfulExit</key>\n    <false/>\n  </dict>\n")
	document.WriteString(fmt.Sprintf("  <key>ThrottleInterval</key>\n  <integer>%d</integer>\n", throttleInterval))
	writeKeyString(&document, "StandardOutPath", paths.StdoutLog)
	writeKeyString(&document, "StandardErrorPath", paths.StderrLog)
	document.WriteString("</dict>\n</plist>\n")

	decoder := xml.NewDecoder(bytes.NewReader(document.Bytes()))
	for {
		if _, err := decoder.Token(); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}
	return document.Bytes(), nil
}

func writeKeyString(document *bytes.Buffer, key, value string) {
	document.WriteString("  <key>")
	_ = xml.EscapeText(document, []byte(key))
	document.WriteString("</key>\n")
	writeString(document, value, 2)
}

func writeString(document *bytes.Buffer, value string, indent int) {
	for index := 0; index < indent; index++ {
		document.WriteByte(' ')
	}
	document.WriteString("<string>")
	_ = xml.EscapeText(document, []byte(value))
	document.WriteString("</string>\n")
}
