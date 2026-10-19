package agent

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// CodexAttachment is a pasted image in a Codex user message: the label Codex
// gives it ("[Image #1]"), the base name of the file it saved and a reference to its bytes in
// the transcript (ImageRef), never the image bytes or a full path.
type CodexAttachment struct {
	Name string
	File string
	Ref  string // ImageRef of the image's own bytes, when the transcript holds them as a data URL (#5032)
}

var (
	codexImageOpenRe = regexp.MustCompile(`^\s*<image\b([^>]*)>\s*$`)
	codexImageNameRe = regexp.MustCompile(`name=(\[[^\]]*\]|"[^"]*")`)
	codexImagePathRe = regexp.MustCompile(`path="([^"]*)"`)
)

// CodexUserContent reads the content blocks of a Codex user message. A pasted
// image arrives as an input_text '<image name=[Image #1] path="...">', an
// input_image holding the data URL, and an input_text '</image>'. The wrapper
// tags and every data: URL are left out of the text; each image becomes an
// attachment. An image-only message reads as its labels ("[Image #1]").
func CodexUserContent(content any) (string, []CodexAttachment) {
	blocks, ok := content.([]any)
	if !ok {
		s, _ := content.(string)
		return s, nil
	}
	var parts []string
	var atts []CodexAttachment
	var pendingName, pendingFile string
	for _, block := range blocks {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		switch bt, _ := b["type"].(string); bt {
		case "text", "input_text":
			text, _ := b["text"].(string)
			if text == "" {
				continue
			}
			if m := codexImageOpenRe.FindStringSubmatch(text); m != nil {
				pendingName, pendingFile = codexImageTagFields(m[1])
				continue
			}
			if strings.TrimSpace(text) == "</image>" {
				continue
			}
			parts = append(parts, text)
		case "input_image", "image":
			ref, _ := b["image_url"].(string)
			if ref == "" {
				if m, _ := b["image_url"].(map[string]any); m != nil {
					ref, _ = m["url"].(string)
				}
			}
			if ref == "" {
				ref, _ = b["path"].(string)
			}
			file := pendingFile
			if file == "" {
				file = CodexImageFile(ref)
			}
			att := CodexAttachment{Name: codexImageName(pendingName, len(atts)+1), File: file}
			if payload, ok := DataURLBase64(ref); ok {
				att.Ref = ImageRef(payload)
			}
			atts = append(atts, att)
			pendingName, pendingFile = "", ""
		}
	}
	return codexJoinAttachmentText(strings.Join(parts, "\n"), atts), atts
}

// CodexPayloadImages turns the "images" list of an event_msg user_message
// (paths or data URLs) into attachments.
func CodexPayloadImages(images []any) []CodexAttachment {
	var atts []CodexAttachment
	for _, img := range images {
		if ref, _ := img.(string); ref != "" {
			att := CodexAttachment{Name: codexImageName("", len(atts)+1), File: CodexImageFile(ref)}
			if payload, ok := DataURLBase64(ref); ok {
				att.Ref = ImageRef(payload)
			}
			atts = append(atts, att)
		}
	}
	return atts
}

// CodexUserMessageText is the text of an event_msg user_message: the typed
// message, or the image labels when only images were sent.
func CodexUserMessageText(message string, atts []CodexAttachment) string {
	return codexJoinAttachmentText(message, atts)
}

// CodexImageFile is the base name of an image reference, empty for a data URL
// or anything that is not a file path.
func CodexImageFile(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(strings.ToLower(ref), "data:") || strings.Contains(ref, "://") {
		return ""
	}
	base := path.Base(strings.ReplaceAll(ref, `\`, "/"))
	if base == "." || base == "/" {
		return ""
	}
	return base
}

func codexImageTagFields(attrs string) (name, file string) {
	if m := codexImageNameRe.FindStringSubmatch(attrs); m != nil {
		name = strings.Trim(m[1], `"`)
	}
	if m := codexImagePathRe.FindStringSubmatch(attrs); m != nil {
		file = CodexImageFile(m[1])
	}
	return name, file
}

func codexImageName(name string, n int) string {
	if name != "" {
		return name
	}
	return "[Image #" + strconv.Itoa(n) + "]"
}

func codexJoinAttachmentText(text string, atts []CodexAttachment) string {
	if strings.TrimSpace(text) != "" || len(atts) == 0 {
		return text
	}
	names := make([]string, len(atts))
	for i, a := range atts {
		names[i] = a.Name
	}
	return strings.Join(names, " ")
}
