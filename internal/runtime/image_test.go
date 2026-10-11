package runtime_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/runtimetest"
)

func TestCompletedImageRetainsBytesAndRejectsUnsafeInputs(t *testing.T) {
	for name, encode := range map[string]func(*bytes.Buffer) error{
		"png":  func(b *bytes.Buffer) error { return png.Encode(b, image.NewRGBA(image.Rect(0, 0, 2, 3))) },
		"jpeg": func(b *bytes.Buffer) error { return jpeg.Encode(b, image.NewRGBA(image.Rect(0, 0, 2, 3)), nil) },
		"gif":  func(b *bytes.Buffer) error { return gif.Encode(b, image.NewRGBA(image.Rect(0, 0, 2, 3)), nil) },
	} {
		var b bytes.Buffer
		if err := encode(&b); err != nil {
			t.Fatal(err)
		}
		content, err := dgruntime.ParseCompletedImage(dgruntime.CompletedImage{Data: base64.StdEncoding.EncodeToString(b.Bytes())}.Payload())
		if err != nil || !bytes.Equal(content.Bytes, b.Bytes()) || content.MediaType != "image/"+name || content.Width != 2 || content.Height != 3 {
			t.Fatal("image bytes or dimensions changed", name, err)
		}
	}
	original, _ := base64.StdEncoding.DecodeString(runtimetest.ImageData)
	huge := append([]byte(nil), original...)
	binary.BigEndian.PutUint32(huge[16:20], 8193)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	pixels := append([]byte(nil), original...)
	binary.BigEndian.PutUint32(pixels[16:20], 8192)
	binary.BigEndian.PutUint32(pixels[20:24], 8192)
	binary.BigEndian.PutUint32(pixels[29:33], crc32.ChecksumIEEE(pixels[12:29]))
	for name, data := range map[string]string{
		"empty": "", "invalid-base64": "%%%", "newline": runtimetest.ImageData + "\n", "not-image": base64.StdEncoding.EncodeToString([]byte("private")),
		"truncated": base64.StdEncoding.EncodeToString(original[:40]), "dimension": base64.StdEncoding.EncodeToString(huge), "pixels": base64.StdEncoding.EncodeToString(pixels),
		"oversize": strings.Repeat("A", base64.StdEncoding.EncodedLen(dgruntime.MaximumImageBytes)+4), "svg": base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := dgruntime.ParseCompletedImage(dgruntime.CompletedImage{Data: data}.Payload()); err == nil {
				t.Fatal("unsafe image accepted")
			}
		})
	}
	if _, err := dgruntime.ParseCompletedImage(map[string]any{"data": runtimetest.ImageData, "path": "/private"}); err == nil {
		t.Fatal("native path accepted")
	}
}

func TestImageArtifactRejectsMalformedReferences(t *testing.T) {
	ref := dgruntime.ImageArtifact{ArtifactID: "art_" + strings.Repeat("a", 20), Digest: "sha256:" + strings.Repeat("a", 64), SizeBytes: 70, MediaType: "image/png", Width: 1, Height: 1, Preview: dgruntime.ImagePreview}
	if _, err := dgruntime.ParseImageArtifact(ref.Payload()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"path": func(p map[string]any) { p["path"] = "private" }, "missing": func(p map[string]any) { delete(p, "digest") }, "null": func(p map[string]any) { p["width"] = nil },
		"fraction": func(p map[string]any) { p["height"] = 1.5 }, "oversize": func(p map[string]any) { p["sizeBytes"] = dgruntime.MaximumImageBytes + 1 }, "media": func(p map[string]any) { p["mediaType"] = "text/html" },
		"preview": func(p map[string]any) { p["preview"] = "private" }, "dimensions": func(p map[string]any) { p["width"] = 8192; p["height"] = 8192 },
	} {
		t.Run(name, func(t *testing.T) {
			p := ref.Payload()
			mutate(p)
			if _, err := dgruntime.ParseImageArtifact(p); err == nil {
				t.Fatal("invalid reference accepted")
			}
		})
	}
}

func TestCompletedImageAcceptsGIFSubframeWithinLogicalScreen(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	frame := image.NewPaletted(image.Rect(1, 1, 3, 3), palette)
	var content bytes.Buffer
	if err := gif.EncodeAll(&content, &gif.GIF{Image: []*image.Paletted{frame}, Delay: []int{0}, Config: image.Config{ColorModel: palette, Width: 4, Height: 5}}); err != nil {
		t.Fatal(err)
	}
	parsed, err := dgruntime.ParseCompletedImage(dgruntime.CompletedImage{Data: base64.StdEncoding.EncodeToString(content.Bytes())}.Payload())
	if err != nil || parsed.Width != 4 || parsed.Height != 5 || !bytes.Equal(parsed.Bytes, content.Bytes()) {
		t.Fatal("valid GIF subframe rejected", err)
	}
}
