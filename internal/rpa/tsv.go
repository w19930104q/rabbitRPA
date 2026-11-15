package rpa

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Devices recognised in a replay TSV.
const (
	DeviceKey   = "key"
	DeviceClick = "click"
	DeviceMove  = "move"
)

// ReadTSV strictly reads a replay/recording TSV.
//
// Every physical line must contain exactly four tab separated fields
// (Device, Pre, Aft, Params). Blank lines, malformed lines and an empty
// file are all errors carrying the offending 1-based line number, because a
// silent partial load previously let replays succeed while skipping work.
func ReadTSV(path string) ([]HistoryItem, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open import file %q: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat import file %q: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("import path %q is a directory", path)
	}
	if info.Size() == 0 {
		return nil, fmt.Errorf("import file %q is empty", path)
	}

	var items []HistoryItem
	var errs []error
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for s.Scan() {
		line++
		text := s.Text()
		fields := strings.Split(text, "\t")
		if len(fields) != 4 {
			errs = append(errs, &LineError{Line: line,
				Msg: fmt.Sprintf("malformed TSV: expected 4 tab fields, got %d", len(fields))})
			continue
		}
		items = append(items, HistoryItem{
			Device: fields[0],
			Pre:    fields[1],
			Aft:    fields[2],
			Params: fields[3],
			Line:   line,
		})
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("read import file %q: %w", path, err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("import file %q contains no replay steps", path)
	}
	return items, nil
}

// ParseSteps converts raw TSV items into executable steps and validates
// every parameter up front. Image references are resolved against
// tmpBaseDir (the -tmpDir directory containing the recorded captures) and
// must exist; a missing screenshot or template therefore fails before any
// desktop action runs.
func ParseSteps(items []HistoryItem, tmpBaseDir string) ([]Step, error) {
	var errs []error
	steps := make([]Step, 0, len(items))

	for _, it := range items {
		st := Step{Item: it}
		switch it.Device {
		case DeviceKey:
			if it.Params == "" {
				errs = append(errs, &LineError{Line: it.Line, Msg: "key step has empty params"})
				continue
			}
			st.KeyParams = it.Params
		case DeviceClick:
			// Original exported replay format: allImg;partImg;targetX;targetY;clickType
			// (five fields; allImg is kept for recording-format compatibility but
			// never read during replay). A four-field partImg;targetX;targetY;clickType
			// shorthand is accepted too.
			fields := strings.Split(it.Params, ";")
			for _, f := range fields {
				if f == "" {
					errs = append(errs, &LineError{Line: it.Line, Msg: "click params contain an empty field"})
					break
				}
			}
			var template, sx, sy, sbutton string
			switch len(fields) {
			case 5:
				template, sx, sy, sbutton = fields[1], fields[2], fields[3], fields[4]
			case 4:
				template, sx, sy, sbutton = fields[0], fields[1], fields[2], fields[3]
			default:
				errs = append(errs, &LineError{Line: it.Line,
					Msg: fmt.Sprintf("click params: expected 4 or 5 ';'-separated fields, got %d", len(fields))})
				continue
			}
			x, err := strconv.Atoi(sx)
			if err != nil {
				errs = append(errs, &LineError{Line: it.Line, Msg: "click offset X is not an integer: " + sx})
				continue
			}
			y, err := strconv.Atoi(sy)
			if err != nil {
				errs = append(errs, &LineError{Line: it.Line, Msg: "click offset Y is not an integer: " + sy})
				continue
			}
			button, err := strconv.Atoi(sbutton)
			if err != nil {
				errs = append(errs, &LineError{Line: it.Line, Msg: "click button code is not an integer: " + sbutton})
				continue
			}
			if err := checkImageRef(it.Line, "click template", template, tmpBaseDir); err != nil {
				errs = append(errs, err)
				continue
			}
			st.TemplateFile = template
			st.OffsetX, st.OffsetY, st.Button = x, y, button
		case DeviceMove:
			// Replay (exported) format: partImage;targetX;targetY.
			fields, perr := parseSemicolon(it, 3)
			if perr != nil {
				errs = append(errs, perr)
				continue
			}
			x, err := strconv.Atoi(fields[1])
			if err != nil {
				errs = append(errs, &LineError{Line: it.Line, Msg: "move offset X is not an integer: " + fields[1]})
				continue
			}
			y, err := strconv.Atoi(fields[2])
			if err != nil {
				errs = append(errs, &LineError{Line: it.Line, Msg: "move offset Y is not an integer: " + fields[2]})
				continue
			}
			if err := checkImageRef(it.Line, "move template", fields[0], tmpBaseDir); err != nil {
				errs = append(errs, err)
				continue
			}
			st.TemplateFile = fields[0]
			st.OffsetX, st.OffsetY = x, y
		default:
			errs = append(errs, &LineError{Line: it.Line, Msg: "unknown device type: " + it.Device})
			continue
		}
		steps = append(steps, st)
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return steps, nil
}

func parseSemicolon(it HistoryItem, want int) ([]string, error) {
	fields := strings.Split(it.Params, ";")
	if len(fields) != want {
		return nil, &LineError{Line: it.Line,
			Msg: fmt.Sprintf("%s params: expected %d ';'-separated fields, got %d", it.Device, want, len(fields))}
	}
	for i, f := range fields {
		if f == "" {
			return nil, &LineError{Line: it.Line, Msg: fmt.Sprintf("%s params: field %d is empty", it.Device, i+1)}
		}
	}
	return fields, nil
}

// checkImageRef enforces the original layout (plain capture file names
// inside the tmp directory) and verifies the capture actually exists.
func checkImageRef(line int, label, name, tmpBaseDir string) error {
	if name == "" {
		return &LineError{Line: line, Msg: label + " file name is empty"}
	}
	if filepath.IsAbs(name) || strings.Contains(name, `/`) || strings.Contains(name, `\`) {
		return &LineError{Line: line, Msg: label + " must be a file name inside the tmp directory, got: " + name}
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return &LineError{Line: line, Msg: label + " path escapes the tmp directory: " + name}
	}
	path := filepath.Join(tmpBaseDir, name)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &LineError{Line: line, Msg: label + " image not found: " + path}
		}
		return &LineError{Line: line, Msg: label + " image unreadable: " + path + " (" + err.Error() + ")"}
	}
	if info.IsDir() {
		return &LineError{Line: line, Msg: label + " path is a directory: " + path}
	}
	return nil
}

// ExportHistory writes recorded items in the original TSV recording format.
//
// The record-time in-memory click record has seven fields
// (allImg;allHash;partImg;partHash;x;y;button); the exported replay TSV
// keeps the original five-field shape (allImg;partImg;x;y;button). Replay
// only locates the part template and the click offset/button, so the first
// (all-image) field is retained for format compatibility but not required to
// exist. Move records export as partImg;x;y (three fields).
func ExportHistory(w io.Writer, items []HistoryItem) error {
	bw := bufio.NewWriter(w)
	for _, it := range items {
		line := it.Device + "\t" + it.Pre + "\t" + it.Aft + "\t"
		switch it.Device {
		case DeviceClick:
			s := strings.Split(it.Params, ";")
			if len(s) != 7 {
				return fmt.Errorf("internal click record must have 7 fields, got %d", len(s))
			}
			// Original export indices: allImg;partImg;x;y;button.
			line += s[0] + ";" + s[2] + ";" + s[4] + ";" + s[5] + ";" + s[6]
		case DeviceMove:
			s := strings.Split(it.Params, ";")
			if len(s) != 4 {
				return fmt.Errorf("internal move record must have 4 fields, got %d", len(s))
			}
			line += s[0] + ";" + s[2] + ";" + s[3]
		default:
			line += it.Params
		}
		if _, err := bw.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	return bw.Flush()
}
