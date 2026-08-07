// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

type fieldKind int

const (
	kindScalar fieldKind = iota
	kindSequence
	kindSection
)

var durationType = reflect.TypeFor[time.Duration]()

// GetValue renders a flat list or mapping one entry per line, and anything nested as YAML.
func GetValue(file, path string) (string, error) {
	session, err := loadForEdit(file, path)
	if err != nil {
		return "", err
	}

	node, err := lookupNode(session.root, session.segments)
	if err != nil {
		return "", err
	}

	return renderNode(node)
}

func SetValue(file, path string, values ...string) error {
	session, err := loadForEdit(file, path)
	if err != nil {
		return err
	}

	var replacement *yaml.Node
	switch session.field.kind {
	case kindSequence:
		if len(values) == 0 {
			return fmt.Errorf("%q needs at least one value", path)
		}
		items, err := session.scalars(values)
		if err != nil {
			return err
		}
		replacement = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
	case kindScalar:
		if len(values) != 1 {
			return fmt.Errorf("%q takes exactly one value", path)
		}
		items, err := session.scalars(values)
		if err != nil {
			return err
		}
		replacement = items[0]
	default:
		return fmt.Errorf("%q is a section, set one of its keys instead", path)
	}

	node, err := ensureNode(session.root, session.segments)
	if err != nil {
		return err
	}
	replaceNode(node, replacement)

	return session.write()
}

func AddValue(file, path string, values ...string) error {
	session, err := loadSequenceEdit(file, path, values)
	if err != nil {
		return err
	}
	items, err := session.scalars(values)
	if err != nil {
		return err
	}

	node, err := ensureNode(session.root, session.segments)
	if err != nil {
		return err
	}
	if node.Kind != yaml.SequenceNode {
		replaceNode(node, &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
	}

	for _, item := range items {
		if indexOfValue(node, item.Value) >= 0 {
			return fmt.Errorf("%s already contains %q", path, item.Value)
		}
		node.Content = append(node.Content, item)
	}

	return session.write()
}

func RemoveValue(file, path string, values ...string) error {
	session, err := loadSequenceEdit(file, path, values)
	if err != nil {
		return err
	}
	items, err := session.scalars(values)
	if err != nil {
		return err
	}

	node, err := lookupNode(session.root, session.segments)
	if err != nil {
		return err
	}
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s is not a list in %q", path, file)
	}

	for _, item := range items {
		index := indexOfValue(node, item.Value)
		if index < 0 {
			return fmt.Errorf("%s does not contain %q", path, item.Value)
		}
		node.Content = slices.Delete(node.Content, index, index+1)
	}

	return session.write()
}

func indexOfValue(seq *yaml.Node, value string) int {
	for i, item := range seq.Content {
		if item.Kind == yaml.ScalarNode && item.Value == value {
			return i
		}
	}
	return -1
}

// replaceNode assigns field by field, as *node = *with would drop the comments attached to node.
func replaceNode(node, with *yaml.Node) {
	node.Kind, node.Tag, node.Style, node.Value, node.Content = with.Kind, with.Tag, with.Style, with.Value, with.Content
}

type editSession struct {
	file     string
	path     string
	original []byte
	root     *yaml.Node
	field    *fieldInfo
	segments []string
	preamble []byte // text of a file that holds no YAML node, which the encoder cannot give back
}

// loadForEdit validates the path and parses the file, so every caller fails before anything is written.
func loadForEdit(file, path string) (*editSession, error) {
	if path == "" {
		return nil, errors.New("no config key given")
	}
	segments := strings.Split(path, ".")
	field, err := resolvePath(segments)
	if err != nil {
		return nil, err
	}

	content, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config file %q does not exist, create one with `config init`", file)
		}
		return nil, err
	}

	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, fmt.Errorf("parse config file %q: %w", file, err)
	}
	var preamble []byte
	if root.Kind == 0 || len(root.Content) == 0 {
		preamble = bytes.TrimSpace(content) // all the file has is comments
		root = yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
		}
	}
	if root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config file %q is not a YAML mapping", file)
	}

	return &editSession{file: file, path: path, original: content, root: &root, field: field, segments: segments, preamble: preamble}, nil
}

func loadSequenceEdit(file, path string, values []string) (*editSession, error) {
	session, err := loadForEdit(file, path)
	if err != nil {
		return nil, err
	}
	if session.field.kind != kindSequence {
		return nil, fmt.Errorf("%q is not a list, use `config set` instead", path)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%q needs at least one value", path)
	}
	return session, nil
}

func (s *editSession) scalars(values []string) ([]*yaml.Node, error) {
	nodes := make([]*yaml.Node, 0, len(values))
	for _, value := range values {
		node, err := scalarNode(s.field.typ, value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.path, err)
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func lookupNode(root *yaml.Node, segments []string) (*yaml.Node, error) {
	node := root.Content[0]
	for i, segment := range segments {
		if node.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%q is not set", strings.Join(segments[:i], "."))
		}
		value := mappingValue(node, segment)
		if value == nil {
			return nil, fmt.Errorf("%q is not set", strings.Join(segments[:i+1], "."))
		}
		node = value
	}
	return node, nil
}

func ensureNode(root *yaml.Node, segments []string) (*yaml.Node, error) {
	node := root.Content[0]
	for i, segment := range segments {
		if node.Kind != yaml.MappingNode {
			if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
				node.Kind, node.Tag, node.Style, node.Value = yaml.MappingNode, "!!map", 0, ""
			} else {
				return nil, fmt.Errorf("%q is not a section", strings.Join(segments[:i], "."))
			}
		}
		value := mappingValue(node, segment)
		if value == nil {
			value = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
			node.Content = append(node.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: segment},
				value)
		}
		node = value
	}
	return node, nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func renderNode(node *yaml.Node) (string, error) {
	if !allScalars(node.Content) {
		encoded, err := encodeYAML(node)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(encoded), "\n"), nil
	}

	switch node.Kind {
	case yaml.SequenceNode:
		lines := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			lines = append(lines, item.Value)
		}
		return strings.Join(lines, "\n"), nil
	case yaml.MappingNode:
		lines := make([]string, 0, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			lines = append(lines, node.Content[i].Value+"="+node.Content[i+1].Value)
		}
		return strings.Join(lines, "\n"), nil
	default:
		return node.Value, nil
	}
}

func allScalars(nodes []*yaml.Node) bool {
	for _, node := range nodes {
		if node.Kind != yaml.ScalarNode {
			return false
		}
	}
	return true
}

func encodeYAML(node *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// restoreLayout re-applies the spacing the encoder drops: the blank lines between
// top-level sections, and the indentation of comments, which the encoder emits at the
// indentation of the node it attached them to rather than the one they were written at.
func restoreLayout(original, generated []byte) []byte {
	type comment struct {
		line        string // as written, indentation included
		trimmed     string
		blankBefore bool
	}

	var comments []comment
	spaced := map[string]bool{}
	blank := false
	for line := range strings.Lines(string(original)) {
		line = strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			blank = true
		case strings.HasPrefix(trimmed, "#"):
			comments = append(comments, comment{line: line, trimmed: trimmed, blankBefore: blank})
			blank = false
		default:
			if key, ok := topLevelKey(line); ok && blank {
				spaced[key] = true
			}
			blank = false
		}
	}

	var out []string
	appendBlank := func() {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
	}
	for line := range strings.Lines(string(generated)) {
		line = strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if i := slices.IndexFunc(comments, func(c comment) bool { return c.trimmed == trimmed }); i >= 0 {
				if comments[i].blankBefore {
					appendBlank()
				} else if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
					out = out[:len(out)-1] // the encoder separates a comment block it moved
				}
				line = comments[i].line
				comments = comments[i+1:] // the encoder keeps their order, so earlier ones cannot match again
			}
		} else if key, ok := topLevelKey(line); ok && spaced[key] {
			appendBlank()
		}
		out = append(out, line)
	}
	if bytes.HasSuffix(generated, []byte("\n")) {
		out = append(out, "")
	}

	return []byte(strings.Join(out, "\n"))
}

func topLevelKey(line string) (string, bool) {
	if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' || line[0] == '-' {
		return "", false
	}
	key, _, ok := strings.Cut(line, ":")
	return key, ok
}

func (s *editSession) write() error {
	generated, err := encodeYAML(s.root)
	if err != nil {
		return err
	}

	// A file the runner already refused to load stays the user's to fix, only a regression is rejected.
	if err := yaml.Unmarshal(generated, &Config{}); err != nil && yaml.Unmarshal(s.original, &Config{}) == nil {
		return fmt.Errorf("the edit would produce a config the runner cannot load: %w", err)
	}

	if len(s.preamble) > 0 { // before restoreLayout, so that it spaces the preamble too
		generated = slices.Concat(s.preamble, []byte("\n"), generated)
	}

	content := restoreLayout(s.original, generated)
	if bytes.Contains(s.original, []byte("\r\n")) { // the encoder only ever emits LF
		content = bytes.ReplaceAll(content, []byte("\n"), []byte("\r\n"))
	}

	return WriteFile(s.file, content)
}

// WriteFile replaces the config file in one step, keeping the mode and owner of the
// file it replaces, so a half-written config never reaches a runner reading it.
func WriteFile(file string, content []byte) error {
	if resolved, err := filepath.EvalSymlinks(file); err == nil {
		file = resolved // keeps a config linked in from elsewhere intact
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	var info os.FileInfo
	mode := os.FileMode(0o600)
	if stat, err := os.Stat(file); err == nil {
		info, mode = stat, stat.Mode().Perm()
	}

	temp, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())

	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if info != nil { // before the chmod, as a chown can clear mode bits
		if err := preserveOwner(temp.Name(), info); err != nil {
			return err
		}
	}
	if err := os.Chmod(temp.Name(), mode); err != nil {
		return err
	}

	return os.Rename(temp.Name(), file)
}

type fieldInfo struct {
	kind fieldKind
	typ  reflect.Type // the element type for a sequence
}

// resolvePath walks the Config struct through the yaml tags of a dotted path.
func resolvePath(segments []string) (*fieldInfo, error) {
	typ := reflect.TypeFor[Config]()

	for i, segment := range segments {
		switch typ.Kind() {
		case reflect.Struct:
			field, ok := fieldByYAMLName(typ, segment)
			if !ok {
				return nil, fmt.Errorf("unknown config key %q, valid keys here: %s",
					strings.Join(segments[:i+1], "."), strings.Join(yamlNames(typ), ", "))
			}
			typ = field.Type
		case reflect.Map:
			// The segment names a user-defined entry, so the walk ends here.
			if i != len(segments)-1 {
				return nil, fmt.Errorf("%q has no sub-keys", strings.Join(segments[:i+1], "."))
			}
			return &fieldInfo{kind: kindScalar, typ: typ.Elem()}, nil
		default:
			return nil, fmt.Errorf("%q is a value, not a section", strings.Join(segments[:i], "."))
		}
	}

	switch typ.Kind() {
	case reflect.Slice:
		return &fieldInfo{kind: kindSequence, typ: typ.Elem()}, nil
	case reflect.Map, reflect.Struct:
		return &fieldInfo{kind: kindSection}, nil
	default:
		return &fieldInfo{kind: kindScalar, typ: typ}, nil
	}
}

func fieldByYAMLName(typ reflect.Type, name string) (reflect.StructField, bool) {
	for field := range typ.Fields() {
		if yamlName(field) == name {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

func yamlNames(typ reflect.Type) []string {
	names := make([]string, 0, typ.NumField())
	for field := range typ.Fields() {
		if name := yamlName(field); name != "-" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func yamlName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
	if name == "" {
		return strings.ToLower(field.Name)
	}
	return name
}

// scalarNode types the value, so a bad one is reported instead of landing in the file as a string.
func scalarNode(typ reflect.Type, value string) (*yaml.Node, error) {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	if typ == durationType {
		duration, err := time.ParseDuration(value)
		if err != nil {
			return nil, fmt.Errorf("%q is not a duration such as 30s, 5m or 3h", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: duration.String()}, nil
	}

	switch typ.Kind() {
	case reflect.Bool:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("%q is not a boolean", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(parsed)}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		parsed, err := strconv.ParseInt(value, 10, typ.Bits())
		if err != nil {
			return nil, fmt.Errorf("%q is not a valid %s", value, typ.Kind())
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(parsed, 10)}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		parsed, err := strconv.ParseUint(value, 10, typ.Bits())
		if err != nil {
			return nil, fmt.Errorf("%q is not a valid %s", value, typ.Kind())
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatUint(parsed, 10)}, nil
	case reflect.String:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}, nil
	default:
		return nil, fmt.Errorf("unsupported config value type %s", typ)
	}
}
