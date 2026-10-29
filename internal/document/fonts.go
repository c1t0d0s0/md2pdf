package document

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/c1t0d0s0/md2pdf/internal/assets"
	"github.com/c1t0d0s0/md2pdf/internal/errs"
	"github.com/c1t0d0s0/md2pdf/internal/markdown"
)

// fontFaceCSS writes the bundled fonts to dir and returns @font-face rules
// for them and for fonts found in the extra font directories.
func fontFaceCSS(dir string, fontDirs []string, warn func(string)) (string, error) {
	var b strings.Builder
	for _, f := range assets.Fonts {
		data, err := fs.ReadFile(assets.FS(), f.File)
		if err != nil {
			return "", errs.Runtime("bundled font is missing: %s", f.File)
		}
		target := filepath.Join(dir, filepath.Base(f.File))
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return "", errs.Runtime("cannot write font: %v", err)
		}
		fmt.Fprintf(&b, "@font-face { font-family: %s; src: url(%s) format(\"truetype\"); font-weight: %s; font-style: normal; }\n",
			cssString(f.Family), cssString(markdown.FileURL(target)), f.Weight)
	}

	seenFiles := map[string]bool{}
	seenFaces := map[string]bool{}
	for _, d := range fontDirs {
		real, err := filepath.EvalSymlinks(d)
		if err != nil {
			return "", errs.Input("font directory not found: %s", d)
		}
		if st, err := os.Stat(real); err != nil || !st.IsDir() {
			return "", errs.Input("font directory not found: %s", d)
		}
		var files []string
		err = filepath.WalkDir(real, func(p string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !e.IsDir() && fontFileRe.MatchString(e.Name()) {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return "", errs.Input("cannot scan font directory %s: %v", d, err)
		}
		sort.Strings(files)
		for _, file := range files {
			if seenFiles[file] {
				continue
			}
			seenFiles[file] = true
			if strings.HasSuffix(strings.ToLower(file), ".woff2") || strings.HasSuffix(strings.ToLower(file), ".woff") {
				warn(fmt.Sprintf("unsupported font format ignored (WOFF metadata inspection is unavailable): %s", file))
				continue
			}
			faces, err := parseFontFile(file)
			if err != nil {
				return "", errs.Input("cannot use font file %s: %v", file, err)
			}
			for _, face := range faces {
				if face.restricted {
					warn(fmt.Sprintf("font excluded (embedding restricted by OS/2 fsType): %s", file))
					continue
				}
				key := fmt.Sprintf("%s|%d|%s", face.family, face.weight, face.style)
				if seenFaces[key] {
					warn(fmt.Sprintf("duplicate font face ignored: %s (%s %d %s)", file, face.family, face.weight, face.style))
					continue
				}
				seenFaces[key] = true
				fmt.Fprintf(&b, "@font-face { font-family: %s; src: url(%s); font-weight: %d; font-style: %s; }\n",
					cssString(face.family), cssString(markdown.FileURL(file)), face.weight, face.style)
			}
		}
	}
	return b.String(), nil
}

var fontFileRe = regexp.MustCompile(`(?i)\.(ttf|otf|ttc|woff2?)$`)

type fontFace struct {
	family     string
	weight     int
	style      string
	restricted bool
}

// parseFontFile reads the family name, weight, style, and embedding
// permission of each face in a TrueType/OpenType file or collection.
func parseFontFile(file string) ([]fontFace, error) {
	buf, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if len(buf) < 12 {
		return nil, errors.New("font header is truncated")
	}
	if string(buf[:4]) != "ttcf" {
		f, err := parseSfnt(buf, 0)
		if err != nil {
			return nil, err
		}
		return []fontFace{f}, nil
	}
	count := int(binary.BigEndian.Uint32(buf[8:]))
	if count > 4096 || 12+count*4 > len(buf) {
		return nil, errors.New("TTC offset table is truncated")
	}
	var faces []fontFace
	for i := 0; i < count; i++ {
		f, err := parseSfnt(buf, int(binary.BigEndian.Uint32(buf[12+4*i:])))
		if err != nil {
			return nil, err
		}
		faces = append(faces, f)
	}
	return faces, nil
}

func parseSfnt(buf []byte, off int) (fontFace, error) {
	be := binary.BigEndian
	if off < 0 || off+12 > len(buf) {
		return fontFace{}, errors.New("sfnt header offset out of range")
	}
	switch be.Uint32(buf[off:]) {
	case 0x00010000, 0x4f54544f, 0x74727565:
	default:
		return fontFace{}, errors.New("not a recognized sfnt font")
	}
	numTables := int(be.Uint16(buf[off+4:]))
	type table struct{ off, len int }
	tables := map[string]table{}
	for i := 0; i < numTables; i++ {
		rec := off + 12 + 16*i
		if rec+16 > len(buf) {
			return fontFace{}, errors.New("table directory out of range")
		}
		t := table{int(be.Uint32(buf[rec+8:])), int(be.Uint32(buf[rec+12:]))}
		if t.off > len(buf) || t.len > len(buf)-t.off {
			return fontFace{}, errors.New("table out of range")
		}
		tables[string(buf[rec:rec+4])] = t
	}
	name, ok := tables["name"]
	if !ok {
		return fontFace{}, errors.New("missing name table")
	}
	nameTable := buf[name.off : name.off+name.len]
	family := readName(nameTable, 16)
	if family == "" {
		family = readName(nameTable, 1)
	}
	if family == "" {
		return fontFace{}, errors.New("no family name")
	}
	sub := readName(nameTable, 17)
	if sub == "" {
		sub = readName(nameTable, 2)
	}
	face := fontFace{family: family, weight: 400, style: "normal"}
	if os2, ok := tables["OS/2"]; ok && os2.len >= 10 {
		face.weight = int(be.Uint16(buf[os2.off+4:]))
		fsType := be.Uint16(buf[os2.off+8:])
		face.restricted = fsType&0x0002 != 0 || fsType&0x0200 != 0
		if face.weight < 1 || face.weight > 1000 {
			face.weight = 400
		}
	}
	if s := strings.ToLower(sub); strings.Contains(s, "italic") || strings.Contains(s, "oblique") {
		face.style = "italic"
	}
	return face, nil
}

func readName(t []byte, nameID uint16) string {
	be := binary.BigEndian
	if len(t) < 6 {
		return ""
	}
	count := int(be.Uint16(t[2:]))
	strBase := int(be.Uint16(t[4:]))
	best := ""
	for i := 0; i < count; i++ {
		rec := 6 + 12*i
		if rec+12 > len(t) {
			break
		}
		platform, id := be.Uint16(t[rec:]), be.Uint16(t[rec+6:])
		if id != nameID {
			continue
		}
		length, offset := int(be.Uint16(t[rec+8:])), strBase+int(be.Uint16(t[rec+10:]))
		if offset+length > len(t) {
			continue
		}
		raw := t[offset : offset+length]
		switch platform {
		case 0, 3:
			if length%2 != 0 {
				continue
			}
			units := make([]uint16, length/2)
			for j := range units {
				units[j] = be.Uint16(raw[2*j:])
			}
			s := string(utf16.Decode(units))
			if platform == 3 {
				return s
			}
			best = s
		case 1:
			if best == "" {
				best = string(raw)
			}
		}
	}
	return best
}
