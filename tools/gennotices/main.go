// Command gennotices regenerates THIRD-PARTY-NOTICES.txt from the Go module
// cache.
//
// Scope is every module linked into the released artefacts — the modules
// reachable from ./cmd/..., which is what ends up inside the opentams and
// tamsctl binaries and the published container images. Build- and test-only
// modules are excluded because they are never redistributed.
//
// For each module the tool reads the licence file shipped in the module
// cache, classifies it, and pulls out the copyright notices. Modules are then
// grouped by licence family, one section each.
//
// Within a family the licence text is printed once, after the component list,
// rather than repeated under every module. That is the standard attribution
// layout and it still satisfies the reproduce-the-notice conditions: each
// module's own copyright line is listed, and the shared terms follow. A module
// whose licence text is NOT the canonical template for its family is the
// exception — its text is reproduced verbatim in full, so a locally modified
// or unusual licence can never be silently collapsed into the standard one.
//
//	go run ./tools/gennotices          # rewrite THIRD-PARTY-NOTICES.txt
//	go run ./tools/gennotices -check   # fail if the committed files are stale
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	outFile     = "THIRD-PARTY-NOTICES.txt"
	ownModule   = "github.com/amagioss/opentams"
	copyrightCO = "Copyright © 2026 Amagi Media Labs Limited"
)

// familyOrder fixes the order the licence families appear in, both in the
// index table and as files on disk. Alphabetical would put 0BSD first, which
// buries the two families that carry almost every module.
var familyOrder = []string{"MIT", "Apache-2.0", "BSD-3-Clause", "BSD-2-Clause", "0BSD", "ISC", "MPL-2.0"}

// licenseDoc is one licence a module is distributed under. A module usually
// has exactly one; a dual-licensed module has several, either as separate
// files or as several licences inside a single file.
type licenseDoc struct {
	file   string
	family string
	text   string
	// sha256 of the licence file as shipped in the module cache. It anchors
	// this entry to an exact upstream file, so a silent relicensing upstream
	// shows up as a changed digest rather than passing unnoticed.
	sha256    string
	canonical bool
	combined  bool
}

type module struct {
	Path    string
	Version string
	Dir     string

	licenses   []licenseDoc
	copyrights []string
	noticeText string

	// inheritedFrom names the parent module a copyright notice was taken
	// from, when this module ships none of its own.
	inheritedFrom string
}

// entry pairs a module with one of its licences. A dual-licensed module
// produces one entry per licence and so is listed under each family it is
// actually distributed under.
type entry struct {
	mod module
	lic licenseDoc
}

func main() {
	check := flag.Bool("check", false, "verify the committed files are up to date instead of writing them")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}

	mods, err := linkedModules(root)
	if err != nil {
		fatal(err)
	}
	if len(mods) == 0 {
		fatal(fmt.Errorf("no third-party modules resolved from ./cmd/... — is the module cache populated?"))
	}

	for i := range mods {
		if err := mods[i].classify(); err != nil {
			fatal(fmt.Errorf("%s: %w", mods[i].Path, err))
		}
	}

	inheritCopyrights(mods)

	byFamily := map[string][]entry{}
	for _, m := range mods {
		for _, lic := range m.licenses {
			byFamily[lic.family] = append(byFamily[lic.family], entry{mod: m, lic: lic})
		}
	}

	var unknown []string
	for fam, list := range byFamily {
		if !knownFamily(fam) {
			for _, e := range list {
				unknown = append(unknown, e.mod.Path+" ("+fam+")")
			}
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		fatal(fmt.Errorf("unclassified licences, add a template or fix detection:\n  %s",
			strings.Join(unknown, "\n  ")))
	}

	for fam := range byFamily {
		list := byFamily[fam]
		sort.Slice(list, func(i, j int) bool { return list[i].mod.Path < list[j].mod.Path })
	}

	want := render(byFamily, len(mods))
	path := filepath.Join(root, outFile)

	if *check {
		got, err := os.ReadFile(path)
		if err != nil {
			fatal(fmt.Errorf("%s is missing, run `make notices`: %w", outFile, err))
		}
		if string(got) != want {
			fatal(fmt.Errorf("%s is stale, run `make notices`", outFile))
		}
		fmt.Printf("%s is up to date (%d modules, %d licence families)\n", outFile, len(mods), len(byFamily))
		return
	}
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %s: %d modules across %d licence families, %d KiB\n",
		outFile, len(mods), len(byFamily), len(want)/1024)
}

func repoRoot() (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		return "", fmt.Errorf("locating module root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// linkedModules returns the third-party modules reachable from ./cmd/...,
// deduplicated and sorted. Packages from the standard library carry no module
// and are skipped, as is our own module.
func linkedModules(root string) ([]module, error) {
	cmd := exec.Command("go", "list", "-deps", "-json", "./cmd/...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -deps: %w: %s", err, stderr.String())
	}

	type pkg struct {
		Module *struct {
			Path    string
			Version string
			Dir     string
		}
	}

	seen := map[string]module{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("decoding go list output: %w", err)
		}
		if p.Module == nil || p.Module.Dir == "" {
			continue
		}
		if p.Module.Path == ownModule || strings.HasPrefix(p.Module.Path, ownModule+"/") {
			continue
		}
		seen[p.Module.Path] = module{Path: p.Module.Path, Version: p.Module.Version, Dir: p.Module.Dir}
	}

	mods := make([]module, 0, len(seen))
	for _, m := range seen {
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

var (
	// Matches LICENSE, LICENCE, COPYING and their suffixed variants
	// (LICENSE.txt, LICENSE-MIT, LICENSE.libyaml). The suffixed forms are how
	// a dual-licensed module separates its licences into two files, so
	// stopping at the bare name would silently drop one of them.
	licenseNameRE = regexp.MustCompile(`(?i)^(LICEN[CS]E|COPYING)([._-][A-Za-z0-9._-]*)?$`)
	noticeNameRE  = regexp.MustCompile(`(?i)^NOTICE(\.(txt|md))?$`)
	spaceRE       = regexp.MustCompile(`\s+`)
)

func (m *module) classify() error {
	dirEntries, err := os.ReadDir(m.Dir)
	if err != nil {
		return fmt.Errorf("reading module dir: %w", err)
	}

	for _, e := range dirEntries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case licenseNameRE.MatchString(name):
			b, err := os.ReadFile(filepath.Join(m.Dir, name))
			if err != nil {
				return err
			}
			text := string(b)
			fams := detectFamilies(text)
			if len(fams) == 0 {
				return fmt.Errorf("unrecognised licence text in %s", name)
			}
			for _, fam := range fams {
				sum := sha256.Sum256(b)
				m.licenses = append(m.licenses, licenseDoc{
					file:     name,
					family:   fam,
					text:     text,
					sha256:   hex.EncodeToString(sum[:]),
					combined: len(fams) > 1,
					// A file covering several licences can never be replaced
					// by one family's shared text, so it is always reproduced.
					canonical: len(fams) == 1 && matchesCanonical(fam, text),
				})
			}
		case noticeNameRE.MatchString(name) && m.noticeText == "":
			b, err := os.ReadFile(filepath.Join(m.Dir, name))
			if err != nil {
				return err
			}
			m.noticeText = strings.TrimRight(string(b), "\n")
		}
	}
	if len(m.licenses) == 0 {
		return fmt.Errorf("no licence file found in %s", m.Dir)
	}

	sort.Slice(m.licenses, func(i, j int) bool {
		if m.licenses[i].family != m.licenses[j].family {
			return familyRank(m.licenses[i].family) < familyRank(m.licenses[j].family)
		}
		return m.licenses[i].file < m.licenses[j].file
	})
	m.copyrights = m.findCopyrights()
	return nil
}

// detectFamilies returns every licence family present in one text, in
// familyOrder. Most files carry exactly one. Some do not: gopkg.in/yaml.v3
// ships a single LICENSE that opens "This project is covered by two different
// licenses: MIT and Apache" and then reproduces both, because the files ported
// from libyaml keep their original MIT terms. Returning only the first match
// would drop one of the two licences the module is actually under.
func detectFamilies(text string) []string {
	n := normalize(text)
	has := func(s string) bool { return strings.Contains(n, s) }

	var fams []string
	add := func(f string) { fams = append(fams, f) }

	if has("apache license") && has("version 2.0") {
		add("Apache-2.0")
	}
	if has("permission is hereby granted, free of charge") {
		add("MIT")
	}
	if has("permission to use, copy, modify, and/or distribute this software for any purpose") {
		// 0BSD and ISC share this opening. ISC keeps a copyright-retention
		// condition; 0BSD drops every condition.
		if has("provided that the above copyright notice") {
			add("ISC")
		} else {
			add("0BSD")
		}
	}
	if has("redistribution and use in source and binary forms") {
		if has("neither the name") {
			add("BSD-3-Clause")
		} else {
			add("BSD-2-Clause")
		}
	}
	if has("mozilla public license") {
		add("MPL-2.0")
	}

	sort.Slice(fams, func(i, j int) bool { return familyRank(fams[i]) < familyRank(fams[j]) })
	return fams
}

func familyRank(fam string) int {
	for i, f := range familyOrder {
		if f == fam {
			return i
		}
	}
	return len(familyOrder)
}

func knownFamily(fam string) bool {
	for _, f := range familyOrder {
		if f == fam {
			return true
		}
	}
	return false
}

// findCopyrights pulls the copyright notices for a module.
//
// The licence file is the authoritative source for every family except
// Apache-2.0: that licence is a fixed template which asserts no copyright of
// its own, and scanning it only turns up sentences from section 4 that happen
// to contain the word. Apache modules state their copyright in a NOTICE file
// or in source-file headers instead, which is where AWS, Google and the
// Prometheus projects actually put theirs.
func (m *module) findCopyrights() []string {
	for _, lic := range m.licenses {
		if lic.family == "Apache-2.0" {
			continue
		}
		if c := scanCopyrights(lic.text); len(c) > 0 {
			return c
		}
	}
	if c := scanCopyrights(m.noticeText); len(c) > 0 {
		return c
	}
	return m.scanSourceHeaders()
}

// copyrightLineRE matches a line that asserts a copyright, as opposed to prose
// that merely contains the word.
//
// Two things do the work. Anchoring at the start of the line (after any
// comment marker) separates "Copyright 2015 Amazon.com, Inc." from "You may
// add Your own copyright statement to Your modifications". Matching only the
// capitalised word rejects a wrapped prose line that happens to begin with it,
// as gopkg.in/yaml.v3's "copyright staring in 2011 when the project was ported
// over:" does — a notice starts a sentence, so it is always capitalised.
var copyrightLineRE = regexp.MustCompile(`^\s*(//+|#+|\*+|/\*)?\s*(Copyright|COPYRIGHT)\b`)

func scanCopyrights(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !copyrightLineRE.MatchString(line) {
			continue
		}
		// The Apache boilerplate placeholder is not a notice.
		low := strings.ToLower(line)
		if strings.Contains(low, "[yyyy]") || strings.Contains(low, "<year>") ||
			strings.Contains(low, "[name of copyright owner]") {
			continue
		}
		for _, p := range []string{"// ", "//", "# ", "#", "* ", "/* "} {
			if strings.HasPrefix(line, p) {
				line = strings.TrimPrefix(line, p)
				break
			}
		}
		line = strings.TrimSpace(strings.TrimSuffix(line, "*/"))
		if !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
		if len(out) >= 6 {
			break
		}
	}
	return out
}

// inheritCopyrights fills in modules that ship neither a NOTICE nor copyright
// headers — the AWS SDK's many submodules are the case that matters here. Each
// borrows the notice of the longest-prefix parent module that has one, which
// is the project it is actually published by.
func inheritCopyrights(mods []module) {
	byPath := make(map[string][]string, len(mods))
	for _, m := range mods {
		if len(m.copyrights) > 0 {
			byPath[m.Path] = m.copyrights
		}
	}
	for i := range mods {
		if len(mods[i].copyrights) > 0 {
			continue
		}
		best := ""
		for path := range byPath {
			if strings.HasPrefix(mods[i].Path, path+"/") && len(path) > len(best) {
				best = path
			}
		}
		if best != "" {
			mods[i].copyrights = byPath[best]
			mods[i].inheritedFrom = best
		}
	}
}

// scanSourceHeaders looks for a copyright in the comment header of the
// module's Go files. It tries the module root first, then each immediate
// subdirectory: a module like go.mongodb.org/mongo-driver/v2 keeps no Go files
// at its root at all, so a root-only scan would find nothing.
func (m *module) scanSourceHeaders() []string {
	if c := scanDirHeaders(m.Dir); len(c) > 0 {
		return c
	}
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "testdata" {
			continue
		}
		if c := scanDirHeaders(filepath.Join(m.Dir, e.Name())); len(c) > 0 {
			return c
		}
	}
	return nil
}

func scanDirHeaders(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		head := string(b)
		if len(head) > 2048 {
			head = head[:2048]
		}
		// Only the leading comment block counts; a copyright further down the
		// file belongs to vendored or quoted material, not the module.
		if i := strings.Index(head, "\npackage "); i > 0 {
			head = head[:i]
		}
		if c := scanCopyrights(head); len(c) > 0 {
			return c
		}
	}
	return nil
}

// normalize folds a licence text down to what the comparison cares about:
// case, run-length whitespace, and the typographic quotes and dashes that
// upstream projects use interchangeably with their ASCII equivalents.
var quoteFolder = strings.NewReplacer(
	"“", `"`, "”", `"`, "„", `"`,
	"‘", "'", "’", "'",
	"–", "-", "—", "-",
)

func normalize(s string) string {
	return spaceRE.ReplaceAllString(strings.ToLower(quoteFolder.Replace(s)), " ")
}

// grantAnchor is the first phrase of the operative grant in each family's
// text. Everything before it — the licence title, the copyright line, the
// occasional preamble — varies between projects without changing the terms,
// so the comparison starts at the anchor and ignores the lead-in.
var grantAnchor = map[string]string{
	"MIT":          "permission is hereby granted, free of charge",
	"BSD-3-Clause": "redistribution and use in source and binary forms",
	"BSD-2-Clause": "redistribution and use in source and binary forms",
	"0BSD":         "permission to use, copy, modify",
	"ISC":          "permission to use, copy, modify",
}

// canonicalBody reduces a licence text to its operative terms, normalized for
// comparison. A module whose body matches the family template is covered by
// the shared terms printed once per family; anything else gets reproduced in
// full under its own entry.
func canonicalBody(fam, text string) string {
	n := normalize(text)
	if anchor, ok := grantAnchor[fam]; ok {
		if i := strings.Index(n, anchor); i >= 0 {
			n = n[i:]
		}
	}
	return strings.TrimSpace(n)
}

func matchesCanonical(fam, text string) bool {
	tmpl, ok := licenseTemplates[fam]
	if !ok {
		// Apache-2.0 is verified by family detection alone: the text is long,
		// and every module ships the same upstream file.
		return fam == "Apache-2.0"
	}
	return canonicalBody(fam, text) == canonicalBody(fam, tmpl)
}

// render builds the whole THIRD-PARTY-NOTICES file: a preamble, a summary
// table, then one section per licence family in familyOrder.
func render(byFamily map[string][]entry, total int) string {
	var b strings.Builder
	rule := strings.Repeat("=", 78)

	fmt.Fprintf(&b, "%s\nThird-Party Software Licenses\n%s\n\n", rule, rule)
	fmt.Fprintf(&b, "OpenTAMS, %s, is licensed under the\nApache License 2.0. Its full text is in the LICENSE file at the root of this\nrepository.\n\n", copyrightCO)
	fmt.Fprintf(&b, "OpenTAMS uses third-party open-source software components. Each component\nremains subject to its respective copyright and license terms.\n\n")
	fmt.Fprintf(&b, "The following information identifies the applicable third-party components,\nversions, licenses, copyright notices, and license conditions.\n\n")

	fmt.Fprintf(&b, "Scope\n-----\n\n")
	fmt.Fprintf(&b, "These are the %d modules linked into the released opentams and tamsctl\nbinaries and the published container images: every module reachable from\n./cmd/... . Go links statically, so all of them are redistributed inside\nthose artefacts.\n\n", total)
	fmt.Fprintf(&b, "Modules used only to build or test OpenTAMS are not listed. They are never\nredistributed, so no attribution obligation attaches to them.\n\n")

	fmt.Fprintf(&b, "Summary\n-------\n\n")
	for _, fam := range familyOrder {
		if list, ok := byFamily[fam]; ok {
			fmt.Fprintf(&b, "  %-14s %3d %s\n", fam, len(list), plural(len(list), "component", "components"))
		}
	}
	fmt.Fprintf(&b, "\nA dual-licensed module is listed under every license it is distributed\nunder, so these counts add up to more than %d.\n\n", total)

	fmt.Fprintf(&b, "How to read this file\n---------------------\n\n")
	fmt.Fprintf(&b, "Each component gives its module path, version, license, SPDX identifier and\ncopyright notice, followed by the license text that governs it.\n\n")
	fmt.Fprintf(&b, "Components under the Apache License 2.0 do not repeat its text. OpenTAMS is\nitself licensed under Apache 2.0, so a copy travels with every artefact as the\nLICENSE file, which is what section 4(a) asks for. What does vary per module\nis the attribution required by section 4(d), and that is reproduced in full\nwith each component. An Apache-licensed module whose license file is not the\nstandard text is reproduced as well.\n\n")
	fmt.Fprintf(&b, "Every other component reproduces its own license file verbatim, because the\nnotice that has to travel with it is specific to that module: its copyright\nholder, and for BSD 3-Clause the entity named in clause 3.\n\n")

	fmt.Fprintf(&b, "This file is generated from the Go module cache by `make notices`. Do not\nedit it by hand. `make notices-check` fails if it is stale, and CI runs that\ncheck on every pull request.\n\n")

	for _, fam := range familyOrder {
		list, ok := byFamily[fam]
		if !ok {
			continue
		}
		b.WriteString(renderFamily(fam, list))
	}
	return b.String()
}

func renderFamily(fam string, entries []entry) string {
	var b strings.Builder
	rule := strings.Repeat("=", 78)
	sub := strings.Repeat("-", 78)

	fmt.Fprintf(&b, "\n%s\n%s Components\n%s\n\n", rule, familyHeading(fam), rule)
	fmt.Fprintf(&b, "OpenTAMS uses the %d %s listed below under the %s.\n\n",
		len(entries), plural(len(entries), "component", "components"), familyProse(fam))

	if fam == "Apache-2.0" {
		fmt.Fprintf(&b, "License Terms:\n\n")
		fmt.Fprintf(&b, "Licensed under the Apache License, Version 2.0. See the applicable Apache 2.0\nLicense text in the LICENSE file at the root of this repository, also\navailable at http://www.apache.org/licenses/LICENSE-2.0\n\n")
		fmt.Fprintf(&b, "The attribution notices required by section 4(d) are reproduced with each\ncomponent below.\n\n")
	}

	for _, e := range entries {
		m, lic := e.mod, e.lic
		fmt.Fprintf(&b, "%s\n", sub)
		fmt.Fprintf(&b, "Module:    %s\n", m.Path)
		fmt.Fprintf(&b, "Version:   %s\n", m.Version)
		fmt.Fprintf(&b, "License:   %s\n", familyField(fam))
		fmt.Fprintf(&b, "SPDX-License-Identifier: %s\n", fam)
		if len(m.copyrights) == 0 {
			fmt.Fprintf(&b, "Copyright: Not stated separately by the module; see the module source.\n")
		}
		for i, c := range m.copyrights {
			label := "Copyright:"
			if i > 0 {
				label = "          "
			}
			fmt.Fprintf(&b, "%s %s\n", label, c)
		}
		if m.inheritedFrom != "" {
			fmt.Fprintf(&b, "           (this module ships no notice of its own; the notice above is\n            that of %s, the project that publishes it)\n", m.inheritedFrom)
		}
		fmt.Fprintf(&b, "License file: %s (sha256 %s)\n", lic.file, lic.sha256)
		if len(m.licenses) > 1 {
			fmt.Fprintf(&b, "\nThis module is dual-licensed under %s, and is listed under\neach. This entry covers its %s file.\n",
				strings.Join(licenseFamilies(m), " and "), lic.file)
		}
		if fam == "Apache-2.0" && m.noticeText != "" {
			fmt.Fprintf(&b, "\nAttribution notices, reproduced from this module's NOTICE file as required\nby Apache License 2.0 section 4(d):\n\n")
			fmt.Fprintf(&b, "%s\n", indent(m.noticeText, "    "))
		}
		if reproduce(fam, lic) {
			fmt.Fprintf(&b, "\nLicense Terms, reproduced from this module's %s file:\n\n", lic.file)
			fmt.Fprintf(&b, "%s\n", indent(strings.TrimRight(lic.text, "\n"), "    "))
		}
		fmt.Fprintf(&b, "\n")
	}
	return b.String()
}

// reproduce decides whether a component's own licence text is printed.
//
// Everything is reproduced except a stock Apache-2.0 licence file. Repeating
// the Apache text would add roughly 10 KB per module for ~30 modules, all of
// it identical to the repository's own LICENSE, which already travels with
// every artefact and is what section 4(a) requires. A non-standard or
// multi-licence Apache file is still reproduced, because then the text is no
// longer the one in LICENSE.
func reproduce(fam string, lic licenseDoc) bool {
	if fam != "Apache-2.0" {
		return true
	}
	return lic.combined || !lic.canonical
}

// familyNames carries the three ways each licence has to be named: the
// section heading, the value of the per-component "License:" field, and the
// form that reads correctly inside a sentence.
var familyNames = map[string]struct{ heading, field, prose string }{
	"MIT":          {"MIT License", "The MIT License", "MIT License"},
	"Apache-2.0":   {"Apache 2.0 License", "Apache 2.0 License", "Apache License, Version 2.0"},
	"BSD-3-Clause": {"BSD 3-Clause License", "The BSD 3-Clause License", "BSD 3-Clause License"},
	"BSD-2-Clause": {"BSD 2-Clause License", "The BSD 2-Clause License", "BSD 2-Clause License"},
	"0BSD":         {"Zero-Clause BSD License", "Zero-Clause BSD License", "Zero-Clause BSD License"},
	"ISC":          {"ISC License", "ISC License", "ISC License"},
	"MPL-2.0":      {"Mozilla Public License 2.0", "Mozilla Public License 2.0", "Mozilla Public License 2.0"},
}

// licenseFamilies lists the distinct licence families a module is under.
func licenseFamilies(m module) []string {
	var out []string
	seen := map[string]bool{}
	for _, lic := range m.licenses {
		if !seen[lic.family] {
			seen[lic.family] = true
			out = append(out, lic.family)
		}
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func familyHeading(fam string) string { return familyNames[fam].heading }
func familyField(fam string) string   { return familyNames[fam].field }
func familyProse(fam string) string   { return familyNames[fam].prose }

func indent(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "gennotices: %v\n", err)
	os.Exit(1)
}
